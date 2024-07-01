package discord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/davecgh/go-spew/spew"
	ffmpeg_go "github.com/u2takey/ffmpeg-go"
	"github.com/warmans/tvgif/pkg/limits"
	"github.com/warmans/tvgif/pkg/mediacache"
	model2 "github.com/warmans/tvgif/pkg/model"
	"github.com/warmans/tvgif/pkg/search"
	"github.com/warmans/tvgif/pkg/searchterms"
	"github.com/warmans/tvgif/pkg/store"
	"github.com/warmans/tvgif/pkg/util"
	"io"
	"log"
	"log/slog"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Command string

const (
	CommandSearch Command = "tvgif"
	CommandDelete Command = "tvgif-delete"
)

type Action string

const (
	ActionConfirmPostGif      = Action("cfrmg")
	ActionOpenCustomTextModal = Action("cstm")
	ActionUpdatePreview       = Action("upd")
)

type OutputFileType string

const (
	OutputGif  = OutputFileType("gif")
	OutputWebm = OutputFileType("webm")
)

var postedByUser = regexp.MustCompile(`.+ posted by \x60([^\x60]+)\x60`)

var rendersInProgress = map[string]string{}
var renderMutex = sync.RWMutex{}
var errRenderInProgress = errors.New("render in progress")
var errDuplicateInteraction = errors.New("interaction already processing")

func resolveResponseOptions(opts ...responseOption) *responseOptions {
	options := &responseOptions{
		username:       "unknown",
		outputFileType: OutputGif,
	}
	for _, o := range opts {
		o(options)
	}
	return options
}

type responseOptions struct {
	username       string
	customText     []string
	outputFileType OutputFileType
	placeholder    bool
}

type responseOption func(options *responseOptions)

func withUsername(username string) responseOption {
	return func(options *responseOptions) {
		options.username = username
	}
}

func withCustomText(customText []string) responseOption {
	return func(options *responseOptions) {
		options.customText = customText
	}
}

func withVideo(video bool) responseOption {
	return func(options *responseOptions) {
		if video {
			options.outputFileType = OutputWebm
		} else {
			options.outputFileType = OutputGif
		}
	}
}

func withPlaceholder() responseOption {
	return func(options *responseOptions) {
		options.placeholder = true
	}
}

func lockRenderer(username string, interactionIdentifier string) (func(), error) {
	renderMutex.Lock()
	defer renderMutex.Unlock()
	if oldInteractionID, found := rendersInProgress[username]; found {
		if interactionIdentifier == oldInteractionID {
			return func() {}, errDuplicateInteraction
		}
		return func() {}, errRenderInProgress
	}
	rendersInProgress[username] = interactionIdentifier
	return func() {
		renderMutex.Lock()
		delete(rendersInProgress, username)
		renderMutex.Unlock()
	}, nil
}

func NewBot(
	logger *slog.Logger,
	session *discordgo.Session,
	searcher search.Searcher,
	mediaCache *mediacache.Cache,
	mediaPath string,
	botUsername string,
	srtStore *store.SRTStore,
) (*Bot, error) {

	publications, err := searcher.ListTerms(context.Background(), "publication")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch publications: %w", err)
	}
	publicationChoices := []*discordgo.ApplicationCommandOptionChoice{}
	for _, v := range publications {
		if len(publicationChoices) < 25 {
			publicationChoices = append(publicationChoices, &discordgo.ApplicationCommandOptionChoice{
				Name:  v,
				Value: v,
			})
		}
	}

	bot := &Bot{
		logger:      logger,
		session:     session,
		searcher:    searcher,
		mediaCache:  mediaCache,
		mediaPath:   mediaPath,
		srtStore:    srtStore,
		botUsername: botUsername,
		commands: []*discordgo.ApplicationCommand{
			{
				Name:        string(CommandSearch),
				Description: "Search for a TV show gif",
				Type:        discordgo.ChatApplicationCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{
						Name:         "query",
						Description:  `Enter a partial quote. Phrase match with "double quotes". Filter with ~publication #s1e01 +10m30s`,
						Type:         discordgo.ApplicationCommandOptionString,
						Required:     true,
						Autocomplete: true,
					},
				},
			},
			{
				Name: string(CommandDelete),
				Type: discordgo.MessageApplicationCommand,
			},
		},
	}
	bot.commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		string(CommandSearch): bot.queryBegin,
		string(CommandDelete): bot.deletePost,
	}
	bot.buttonHandlers = map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, suffix string){
		ActionConfirmPostGif:      bot.postGif,
		ActionOpenCustomTextModal: bot.editModal,
		ActionUpdatePreview:       bot.updatePreview,
	}
	bot.modalHandlers = map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, suffix string){
		ActionConfirmPostGif: bot.postCustomGif,
	}

	return bot, nil
}

type Bot struct {
	logger          *slog.Logger
	session         *discordgo.Session
	searcher        search.Searcher
	mediaCache      *mediacache.Cache
	mediaPath       string
	srtStore        *store.SRTStore
	botUsername     string
	commands        []*discordgo.ApplicationCommand
	commandHandlers map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate)
	buttonHandlers  map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, customIdPayload string)
	modalHandlers   map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, customIdPayload string)
	createdCommands []*discordgo.ApplicationCommand
}

func (b *Bot) Start() error {
	b.session.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Printf("Logged in as: %v#%v", s.State.User.Username, s.State.User.Discriminator)
	})
	b.session.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			// exact match
			if h, ok := b.commandHandlers[i.ApplicationCommandData().Name]; ok {
				h(s, i)
			}
		case discordgo.InteractionApplicationCommandAutocomplete:
			// exact match
			if h, ok := b.commandHandlers[i.ApplicationCommandData().Name]; ok {
				h(s, i)
			}
		case discordgo.InteractionModalSubmit:
			// prefix match buttons to allow additional data in the customID
			for k, h := range b.modalHandlers {
				actionPrefix := fmt.Sprintf("%s:", k)
				if strings.HasPrefix(i.ModalSubmitData().CustomID, actionPrefix) {
					h(s, i, strings.TrimPrefix(i.ModalSubmitData().CustomID, actionPrefix))
					return
				}
			}
			b.respondError(s, i, fmt.Errorf("unknown customID format: %s", i.MessageComponentData().CustomID))
			return
		case discordgo.InteractionMessageComponent:
			// prefix match buttons to allow additional data in the customID
			for k, h := range b.buttonHandlers {
				actionPrefix := fmt.Sprintf("%s:", k)
				if strings.HasPrefix(i.MessageComponentData().CustomID, actionPrefix) {
					h(s, i, strings.TrimPrefix(i.MessageComponentData().CustomID, actionPrefix))
					return
				}
			}
			b.respondError(s, i, fmt.Errorf("unknown customID format: %s", i.MessageComponentData().CustomID))
			return
		}
	})
	if err := b.session.Open(); err != nil {
		return fmt.Errorf("failed to open session: %w", err)
	}
	var err error
	b.createdCommands, err = b.session.ApplicationCommandBulkOverwrite(b.session.State.User.ID, "", b.commands)
	if err != nil {
		return fmt.Errorf("cannot register commands: %w", err)
	}
	return nil
}

func (b *Bot) Close() error {
	// cleanup commands
	for _, cmd := range b.createdCommands {
		err := b.session.ApplicationCommandDelete(b.session.State.User.ID, "", cmd.ID)
		if err != nil {
			return fmt.Errorf("cannot delete %s command: %w", cmd.Name, err)
		}
	}
	return b.session.Close()
}

func (b *Bot) deletePost(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data, ok := i.Data.(discordgo.ApplicationCommandInteractionData)
	if !ok {
		b.respondError(s, i, fmt.Errorf("wrong message type recieved: %T", i.Data))
		return
	}

	author := i.Data.(discordgo.ApplicationCommandInteractionData).Resolved.Messages[data.TargetID].Author
	if author.String() != b.botUsername {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("Failed: Message doesn't belong to %s", b.botUsername),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			b.respondError(s, i, fmt.Errorf("failed to create response"))
		}
		return
	}

	msgContent := i.Data.(discordgo.ApplicationCommandInteractionData).Resolved.Messages[data.TargetID].Content
	results := postedByUser.FindStringSubmatch(msgContent)
	if len(results) != 2 {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Failed: Couldn't identify poster",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			b.respondError(s, i, fmt.Errorf("failed to create response"))
		}
		return
	}
	if results[1] != uniqueUser(i.Member) {
		err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "Failed: you didn't post that gif",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		if err != nil {
			b.respondError(s, i, fmt.Errorf("failed to create response"))
		}
		return
	}

	if err := s.ChannelMessageDelete(i.ChannelID, data.TargetID); err != nil {
		b.respondError(s, i, fmt.Errorf("failed to delete message: %w", err))
		return
	}
	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "Deleted!",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to create response"))
	}
}

func (b *Bot) queryBegin(s *discordgo.Session, i *discordgo.InteractionCreate) {

	switch i.Type {
	case discordgo.InteractionApplicationCommand:

		selection := i.ApplicationCommandData().Options[0].StringValue()
		if selection == "" {
			b.respondError(s, i, fmt.Errorf("dialog was not selected"))
			return
		}
		username := "unknown"
		if i.Member != nil {
			username = uniqueUser(i.Member)
		}
		customID, err := parseCustomIDPayload(selection)
		if err != nil {
			b.respondError(s, i, fmt.Errorf("invalid selection"))
			return
		}
		dialog, err := b.srtStore.GetDialogRange(customID.Publication, customID.Series, customID.Episode, customID.StartPosition, customID.EndPosition)
		if err != nil {
			b.respondError(
				s,
				i,
				fmt.Errorf("failed to fetch selected lines: %s", customID.String()),
				slog.String("err", err.Error()),
				slog.String("custom_id", customID.String()),
			)
			return
		}
		if len(dialog) == 0 {
			b.respondError(s, i, fmt.Errorf("no dialog was selected"), slog.String("custom_id", customID.String()))
			return
		}
		if err := b.createGifPreview(s, i, dialog, username, customID); err != nil {
			b.logger.Error("Failed to begin video response", slog.String("err", err.Error()))
		}
		return
	case discordgo.InteractionApplicationCommandAutocomplete:
		data := i.ApplicationCommandData()

		rawTerms := strings.TrimSpace(data.Options[0].StringValue())

		terms, err := searchterms.Parse(rawTerms)
		if err != nil {
			return
		}
		if len(terms) == 0 {
			b.logger.Warn("No terms were given")
			if err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionApplicationCommandAutocompleteResult,
				Data: &discordgo.InteractionResponseData{
					Choices: []*discordgo.ApplicationCommandOptionChoice{},
				},
			}); err != nil {
				b.logger.Error("Failed to respond with autocomplete options", slog.String("err", err.Error()))
			}
			return
		}

		f := searchterms.TermsToFilter(terms)
		res, err := b.searcher.Search(
			context.Background(),
			f,
			0,
		)
		if err != nil {
			b.logger.Error("Failed to fetch autocomplete options", slog.String("err", err.Error()))
			return
		}

		var choices []*discordgo.ApplicationCommandOptionChoice
		for _, v := range res {
			name := fmt.Sprintf("[%s] %s", v.EpisodeID, v.Content)
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
				Name:  util.TrimToN(name, 100),
				Value: v.ID,
			})
		}
		if err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionApplicationCommandAutocompleteResult,
			Data: &discordgo.InteractionResponseData{
				Choices: choices,
			},
		}); err != nil {
			b.logger.Error("Failed to respond with autocomplete options", slog.String("err", err.Error()))
		}
		return
	}
	b.respondError(s, i, fmt.Errorf("unknown command type"))
}

func (b *Bot) updatePreview(s *discordgo.Session, i *discordgo.InteractionCreate, customIDPayload string) {
	b.logger.Info("Editing...")
	username := "unknown"
	if i.Member != nil {
		username = uniqueUser(i.Member)
	}

	customID, err := parseCustomIDPayload(customIDPayload)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to parse customID: %w", err))
		return
	}

	dialog, err := b.srtStore.GetDialogRange(customID.Publication, customID.Series, customID.Episode, customID.StartPosition, customID.EndPosition)
	if err != nil {
		b.respondError(
			s,
			i,
			fmt.Errorf("failed to fetch selected lines: %s", customID.String()),
			slog.String("err", err.Error()),
			slog.String("custom_id", customIDPayload),
		)
		return
	}
	if len(dialog) == 0 {
		b.respondError(s, i, fmt.Errorf("no dialog was selected"))
		return
	}

	interactionResponse, err := b.buildInteractionResponse(dialog, customID, withUsername(username), withPlaceholder())
	if err != nil {
		if errors.Is(err, errDuplicateInteraction) {
			return
		}
		b.logger.Error("interaction failed", slog.String("err", err.Error()))
		_, err := s.InteractionResponseEdit(
			i.Interaction,
			&discordgo.WebhookEdit{
				Content: util.ToPtr(fmt.Sprintf("Failed (%s)...", err.Error())),
			},
		)
		if err != nil {
			b.logger.Error("edit failed", slog.String("err", err.Error()))
		}
		return
	}
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: interactionResponse.Data,
	}); err != nil {
		b.respondError(s, i, err)
		return
	}
	go func() {
		interactionResponse, err = b.buildInteractionResponse(dialog, customID, withUsername(username))
		if err != nil {
			if errors.Is(err, errDuplicateInteraction) {
				return
			}
			b.logger.Error("interaction failed", slog.String("err", err.Error()))
			_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: util.ToPtr("Failed....")})
			if err != nil {
				b.logger.Error("edit failed", slog.String("err", err.Error()))
			}
			return
		}
		buttons, err := b.createButtons(dialog, customID)
		if err != nil {
			b.logger.Error("interaction failed", slog.String("err", err.Error()))
			_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: util.ToPtr("Failed....")})
			if err != nil {
				b.logger.Error("edit failed", slog.String("err", err.Error()))
			}
			return
		}
		_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content:    util.ToPtr(interactionResponse.Data.Content),
			Files:      interactionResponse.Data.Files,
			Components: util.ToPtr(buttons),
		})
		if err != nil {
			b.logger.Error("edit failed", slog.String("err", err.Error()))
		}
	}()
}

func (b *Bot) createGifPreview(
	s *discordgo.Session,
	i *discordgo.InteractionCreate,
	dialog []model2.Dialog,
	username string,
	customID *customIdPayload,
) error {
	// send a placeholder
	interactionResponse, err := b.buildInteractionResponse(dialog, customID, withUsername(username), withPlaceholder())
	if err != nil {
		if errors.Is(err, errDuplicateInteraction) {
			fmt.Println("Duplicated interaction")
			return nil
		}
		b.respondError(s, i, err)
		return err
	}
	interactionResponse.Data.Flags = discordgo.MessageFlagsEphemeral
	if err = s.InteractionRespond(i.Interaction, interactionResponse); err != nil {
		b.logger.Error("failed to respond", slog.String("err", err.Error()))
		return err
	}

	// update with the gif
	go func() {
		interactionResponse, err = b.buildInteractionResponse(dialog, customID, withUsername(username))
		if err != nil {
			if errors.Is(err, errDuplicateInteraction) {
				return
			}
			b.logger.Error("interaction failed", slog.String("err", err.Error()))
			_, err := s.InteractionResponseEdit(
				i.Interaction,
				&discordgo.WebhookEdit{
					Content: util.ToPtr(fmt.Sprintf("Failed (%s)...", err.Error())),
				},
			)
			if err != nil {
				b.logger.Error("edit failed", slog.String("err", err.Error()))
			}
			return
		}

		buttons, err := b.createButtons(dialog, customID)
		if err != nil {
			b.logger.Error("edit failed. Failed to create buttons", slog.String("err", err.Error()))
			return
		}
		_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content:    util.ToPtr("Complete!" + interactionResponse.Data.Content),
			Components: util.ToPtr(buttons),
			Files:      interactionResponse.Data.Files,
		})
		if err != nil {
			b.logger.Error("edit failed", slog.String("err", err.Error()))
			return
		}
	}()
	return nil
}

func (b *Bot) editModal(s *discordgo.Session, i *discordgo.InteractionCreate, customIDPayload string) {
	customID, err := parseCustomIDPayload(customIDPayload)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid customID"))
		return
	}
	dialog, err := b.srtStore.GetDialogRange(customID.Publication, customID.Series, customID.Episode, customID.StartPosition, customID.EndPosition)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to fetch original dialog"))
		return
	}

	fields := []discordgo.MessageComponent{}
	for k, d := range dialog {
		fields = append(
			fields,
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.TextInput{
						CustomID:  fmt.Sprintf("custom_text_%d", k),
						Label:     fmt.Sprintf("Gif Text %d", k),
						Style:     discordgo.TextInputParagraph,
						Required:  false,
						MaxLength: 255,
						Value:     d.Content,
					},
				},
			})
	}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID:   encodeAction(ActionConfirmPostGif, customID),
			Title:      "Edit and Post GIF (no preview)",
			Components: fields,
		},
	})
	if err != nil {
		b.respondError(s, i, err)
	}
}

func (b *Bot) createButtons(dialog []model2.Dialog, customID *customIdPayload) ([]discordgo.MessageComponent, error) {

	before, after, err := b.srtStore.GetDialogContext(customID.Publication, customID.Series, customID.Episode, customID.StartPosition, customID.EndPosition)
	if err != nil {
		return nil, err
	}

	dialogDuration := (dialog[len(dialog)-1].EndTimestamp - dialog[0].StartTimestamp) + customID.ExtendOrTrim

	navigateButtons := []discordgo.MessageComponent{}
	if len(before) > 0 {
		prevCustomID, err := parseCustomIDPayload(before[0].ID(customID.EpisodeID()))
		if err != nil {
			return nil, err
		}
		//if dialogDuration+(before[0].EndTimestamp-before[0].StartTimestamp) <= limits.MaxGifDuration && len(dialog) < 5 {
		//	navigateButtons = append(navigateButtons, discordgo.Button{
		//		Label: "Merge Previous Subtitle",
		//		Emoji: &discordgo.ComponentEmoji{
		//			Name: "➕",
		//		},
		//		Style:    discordgo.SecondaryButton,
		//		Disabled: false,
		//		CustomID: encodeAction(ActionUpdatePreview, customID.WithStartPosition(prevCustomID.StartPosition)),
		//	})
		//}
		navigateButtons = append(navigateButtons, discordgo.Button{
			Label: "Previous Subtitle",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏪",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, prevCustomID),
		})
	}
	if len(after) > 0 {
		nextCustomID, err := parseCustomIDPayload(after[0].ID(customID.EpisodeID()))
		if err != nil {
			return nil, err
		}
		navigateButtons = append(navigateButtons, discordgo.Button{
			Label: "Next Subtitle",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithStartPosition(customID.StartPosition+1)),
		})
		if dialogDuration+(after[0].EndTimestamp-after[0].StartTimestamp) <= limits.MaxGifDuration && len(dialog) < 5 {
			navigateButtons = append(navigateButtons, discordgo.Button{
				Label: "Merge Next Subtitle",
				Emoji: &discordgo.ComponentEmoji{
					Name: "➕",
				},
				Style:    discordgo.SecondaryButton,
				Disabled: false,
				CustomID: encodeAction(ActionUpdatePreview, customID.WithEndPosition(nextCustomID.StartPosition)),
			})
		}
	}

	//todo: need the total duration to avoid shifting past the end of the webm
	shiftButtons := []discordgo.MessageComponent{
		discordgo.Button{
			Label: "5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏪",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Shift+(0-(time.Second*5)))),
		},
		discordgo.Button{
			Label: "1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏪",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Shift+(0-time.Second))),
		},
		discordgo.Button{
			Label: "0.5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Shift+(time.Second/2))),
		},
		discordgo.Button{
			Label: "1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Shift+time.Second)),
		},
		discordgo.Button{
			Label: "5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Shift+(time.Second*5))),
		},
	}
	extendButtons := []discordgo.MessageComponent{}
	if dialogDuration+(time.Second/2) <= limits.MaxGifDuration {
		extendButtons = append(extendButtons, discordgo.Button{
			Label: "0.5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "➕",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.ExtendOrTrim+(time.Second/2))),
		})
	}
	if dialogDuration+time.Second <= limits.MaxGifDuration {
		extendButtons = append(extendButtons, discordgo.Button{
			Label: "1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "➕",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.ExtendOrTrim+time.Second)),
		})
	}
	if dialogDuration+(time.Second*5) <= limits.MaxGifDuration {
		extendButtons = append(extendButtons, discordgo.Button{
			Label: "5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "➕",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.ExtendOrTrim+(time.Second*5))),
		})
	}
	if dialogDuration+(time.Second*10) <= limits.MaxGifDuration {
		extendButtons = append(extendButtons, discordgo.Button{
			Label: "10s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "➕",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.ExtendOrTrim+(time.Second*10))),
		})
	}
	trimButtons := []discordgo.MessageComponent{}
	if dialogDuration-(time.Second/2) > 0 {
		trimButtons = append(trimButtons, discordgo.Button{
			Label: "0.5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "✂",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.ExtendOrTrim-(time.Second/2))),
		})
	}
	if dialogDuration-time.Second > 0 {
		trimButtons = append(trimButtons, discordgo.Button{
			Label: "1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "✂",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.ExtendOrTrim-time.Second)),
		})
	}
	if dialogDuration-(time.Second*5) > 0 {
		trimButtons = append(trimButtons, discordgo.Button{
			Label: "5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "✂",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.ExtendOrTrim-(time.Second*5))),
		})
	}

	if customID.StartPosition != customID.EndPosition {
		trimButtons = append(trimButtons, discordgo.Button{
			Label: "Merged Subtitles",
			Emoji: &discordgo.ComponentEmoji{
				Name: "✂",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(
				ActionUpdatePreview,
				customID.
					WithStartPosition(customID.StartPosition).
					WithEndPosition(customID.StartPosition)),
		})
	}

	actions := []discordgo.MessageComponent{}
	if len(navigateButtons) > 0 {
		actions = append(actions, discordgo.ActionsRow{Components: navigateButtons})
	}
	if len(shiftButtons) > 0 {
		actions = append(actions, discordgo.ActionsRow{Components: shiftButtons})
	}
	if len(extendButtons) > 0 {
		actions = append(actions, discordgo.ActionsRow{Components: extendButtons})
	}
	if len(trimButtons) > 0 {
		actions = append(actions, discordgo.ActionsRow{Components: trimButtons})
	}
	actions = append(actions, discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.Button{
				// Label is what the user will see on the button.
				Label: "Post GIF",
				Emoji: &discordgo.ComponentEmoji{
					Name: "✅",
				},
				// Style provides coloring of the button. There are not so many styles tho.
				Style: discordgo.PrimaryButton,
				// Disabled allows bot to disable some buttons for users.
				Disabled: false,
				// CustomID is a thing telling Discord which data to send when this button will be pressed.
				CustomID: encodeAction(ActionConfirmPostGif, customID),
			},
			discordgo.Button{
				// Label is what the user will see on the button.
				Label: "Post GIF with Custom Text",
				Emoji: &discordgo.ComponentEmoji{
					Name: "✅",
				},
				// Style provides coloring of the button. There are not so many styles tho.
				Style: discordgo.PrimaryButton,
				// Disabled allows bot to disable some buttons for users.
				Disabled: false,
				// CustomID is a thing telling Discord which data to send when this button will be pressed.
				CustomID: encodeAction(ActionOpenCustomTextModal, customID),
			},
		},
	})

	spew.Dump(actions)

	return actions, nil
}

func (b *Bot) postCustomGif(s *discordgo.Session, i *discordgo.InteractionCreate, customIDPayload string) {
	if customIDPayload == "" {
		b.respondError(s, i, fmt.Errorf("missing customID"))
		return
	}
	customID, err := parseCustomIDPayload(customIDPayload)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid customID"))
		return
	}
	username := "unknown"
	if i.Member != nil {
		username = uniqueUser(i.Member)
	}
	dialog, err := b.srtStore.GetDialogRange(customID.Publication, customID.Series, customID.Episode, customID.StartPosition, customID.EndPosition)
	if err != nil {
		b.respondError(
			s,
			i,
			fmt.Errorf("failed to fetch selected lines: %s", customID.String()),
			slog.String("err", err.Error()),
			slog.String("custom_id", customIDPayload),
		)
		return
	}
	var customText []string
	for k := range i.Interaction.ModalSubmitData().Components {
		customText = append(customText, i.Interaction.ModalSubmitData().Components[k].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value)
	}
	if len(dialog) == 0 {
		b.respondError(s, i, fmt.Errorf("no dialog was selected"))
		return
	}
	if err = b.completeResponse(s, i, dialog, username, customText, customID, false); err != nil {
		b.respondError(s, i, err)
	}
}

func (b *Bot) postGif(s *discordgo.Session, i *discordgo.InteractionCreate, customIDPayload string) {

	if i.Type != discordgo.InteractionMessageComponent {
		return
	}
	if customIDPayload == "" {
		b.respondError(s, i, fmt.Errorf("missing customID"))
		return
	}
	customID, err := parseCustomIDPayload(customIDPayload)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid customID"))
		return
	}
	username := "unknown"
	if i.Member != nil {
		username = uniqueUser(i.Member)
	}
	dialog, err := b.srtStore.GetDialogRange(customID.Publication, customID.Series, customID.Episode, customID.StartPosition, customID.EndPosition)
	if err != nil {
		b.respondError(
			s,
			i,
			fmt.Errorf("failed to fetch selected lines: %s", customID.String()),
			slog.String("err", err.Error()),
			slog.String("custom_id", customIDPayload),
		)
		return
	}
	if err := b.completeResponse(s, i, dialog, username, nil, customID, false); err != nil {
		b.logger.Error("Failed to complete video response", slog.String("err", err.Error()))
	}
}

func (b *Bot) completeResponse(
	s *discordgo.Session,
	i *discordgo.InteractionCreate,
	dialog []model2.Dialog,
	username string,
	customText []string,
	customID *customIdPayload,
	video bool,
) error {
	interactionResponse, err := b.buildInteractionResponse(dialog, customID, withUsername(username), withPlaceholder(), withVideo(video))
	if err != nil {
		if errors.Is(err, errDuplicateInteraction) {
			fmt.Println("Duplicated interaction")
			return nil
		}
		if errors.Is(err, errRenderInProgress) {
			b.respondError(s, i, errors.New("you already have a render in progress"))
		}
		b.respondError(s, i, err)
		return err
	}
	if err = s.InteractionRespond(i.Interaction, interactionResponse); err != nil {
		return fmt.Errorf("failed to respond: %w", err)
	}
	go func() {
		interactionResponse, err = b.buildInteractionResponse(dialog, customID, withUsername(username), withCustomText(customText), withVideo(video))
		if err != nil {
			if errors.Is(err, errDuplicateInteraction) {
				return
			}
			b.logger.Error("interaction failed", slog.String("err", err.Error()))
			_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: util.ToPtr("Failed....")})
			if err != nil {
				b.logger.Error("edit failed", slog.String("err", err.Error()))
			}
			return
		}
		_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: util.ToPtr(interactionResponse.Data.Content),
			Files:   interactionResponse.Data.Files,
		})
		if err != nil {
			b.logger.Error("edit failed", slog.String("err", err.Error()))
		}
	}()

	return nil
}

func (b *Bot) buildInteractionResponse(
	dialog []model2.Dialog,
	customID *customIdPayload,
	options ...responseOption,
) (*discordgo.InteractionResponse, error) {

	opts := resolveResponseOptions(options...)

	cleanup, err := lockRenderer(opts.username, customID.String())
	defer cleanup()
	if err != nil {
		if errors.Is(err, errDuplicateInteraction) {
			return nil, errDuplicateInteraction
		}
		if errors.Is(err, errRenderInProgress) {
			return nil, errRenderInProgress
		}
		return nil, err
	}

	var files []*discordgo.File

	var bodyText string
	if !opts.placeholder {
		gif, err := b.renderFile(dialog, opts.customText, customID, opts.outputFileType)
		if err != nil {
			return nil, err
		}
		files = []*discordgo.File{gif}
		bodyText = ""
	} else {
		bodyText = ":timer: Rendering..."
	}
	editLabel := ""
	if opts.customText != nil {
		editLabel = " (edited)"
	}
	extendLabel := ""
	if customID.ExtendOrTrim != 0 {
		if customID.ExtendOrTrim > 0 {
			extendLabel = fmt.Sprintf("(+%s)", customID.ExtendOrTrim.String())
		} else {
			extendLabel = fmt.Sprintf("(%s)", customID.ExtendOrTrim.String())
		}
	}
	shiftLabel := ""
	if customID.Shift != 0 {
		if customID.Shift > 0 {
			shiftLabel = fmt.Sprintf("(>>%s)", customID.Shift.String())
		} else {
			shiftLabel = fmt.Sprintf("(<<%s)", customID.Shift.String())
		}
	}
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf(
				"%s\n\n`%s@%s-%s%s%s%s` posted by `%s`",
				bodyText,
				customID.DialogID(),
				dialog[0].StartTimestamp,
				dialog[len(dialog)-1].EndTimestamp,
				shiftLabel,
				extendLabel,
				editLabel,
				opts.username,
			),
			Files:       files,
			Attachments: util.ToPtr([]*discordgo.MessageAttachment{}),
		},
	}, nil
}

func (b *Bot) respondError(s *discordgo.Session, i *discordgo.InteractionCreate, err error, logCtx ...any) {
	b.logger.Error("Error response was sent: "+err.Error(), logCtx...)
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("Request failed with error: %s", err.Error()),
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
	if err != nil {
		b.logger.Error("failed to respond", slog.String("err", err.Error()))
		return
	}
}

func (b *Bot) renderFile(dialog []model2.Dialog, customText []string, customID *customIdPayload, outputFileType OutputFileType) (*discordgo.File, error) {
	disableCaching := customID.ExtendOrTrim != 0 || customID.Shift != 0 || customText != nil

	startTimestamp := dialog[0].StartTimestamp
	endTimestamp := dialog[len(dialog)-1].EndTimestamp

	if customID.Shift != 0 {
		startTimestamp += customID.Shift
		endTimestamp += customID.Shift
	}
	if customID.ExtendOrTrim != 0 {
		endTimestamp += customID.ExtendOrTrim
		if endTimestamp <= startTimestamp {
			endTimestamp = startTimestamp + time.Second
		}
	}
	if endTimestamp-startTimestamp > limits.MaxGifDuration {
		endTimestamp = startTimestamp + limits.MaxGifDuration
	}

	logger := b.logger.With(
		slog.String("cache_key", customID.DialogID()),
		slog.String("source", dialog[0].VideoFileName),
		slog.Duration("from", startTimestamp),
		slog.Duration("to", endTimestamp),
		slog.String("output", string(outputFileType)),
		slog.Bool("custom_text", customText != nil),
	)
	logger.Debug("Exporting gif")

	startTime := time.Now()

	var mimeType string
	var extension string
	buff := &bytes.Buffer{}
	var cacheHit bool
	var err error
	switch outputFileType {
	case OutputWebm:
		mimeType = "video/webm"
		extension = "webm"
		cacheHit, err = b.mediaCache.Get(createFileName(customID, extension), buff, disableCaching, func(writer io.Writer) error {
			err := ffmpeg_go.
				Input(path.Join(b.mediaPath, dialog[0].VideoFileName),
					ffmpeg_go.KwArgs{
						"ss": fmt.Sprintf("%0.2f", startTimestamp.Seconds()),
					}).
				Output("pipe:",
					ffmpeg_go.KwArgs{
						"t":            fmt.Sprintf("%0.2f", endTimestamp.Seconds()-startTimestamp.Seconds()),
						"map_metadata": "-1",
						"format":       "webm",
					},
				).WithOutput(writer, os.Stderr).Run()
			if err != nil {
				b.logger.Error("ffmpeg failed", slog.String("err", err.Error()))
				return err
			}
			return nil
		})
	case OutputGif:
		mimeType = "image/gif"
		extension = "gif"
		cacheHit, err = b.mediaCache.Get(createFileName(customID, extension), buff, disableCaching, func(writer io.Writer) error {
			err := ffmpeg_go.
				Input(path.Join(b.mediaPath, dialog[0].VideoFileName),
					ffmpeg_go.KwArgs{
						"ss": fmt.Sprintf("%0.2f", startTimestamp.Seconds()),
						"to": fmt.Sprintf("%0.2f", endTimestamp.Seconds()),
					}).
				Output("pipe:",
					ffmpeg_go.KwArgs{
						"format":         "gif",
						"filter_complex": createDrawtextFilter(dialog, customText),
					},
				).WithOutput(writer, os.Stderr).Run()
			if err != nil {
				logger.Error("ffmpeg failed", slog.String("err", err.Error()))
				return err
			}

			return nil
		})
	}
	if err != nil {
		logger.Error("cache fetch failed", slog.String("err", err.Error()))
		return nil, err
	}
	logger.Info(
		"Cache result",
		slog.Bool("hit", cacheHit),
		slog.Duration("time taken", time.Since(startTime)),
	)
	return &discordgo.File{
		Name:        createFileName(customID, extension),
		ContentType: mimeType,
		Reader:      buff,
	}, nil
}

func createFileName(customID *customIdPayload, suffix string) string {
	return fmt.Sprintf("%s.%s", customID.DialogID(), suffix)
}

func encodeAction(action Action, customID *customIdPayload) string {
	return fmt.Sprintf("%s:%s", action, customID.String())
}

// formatGifText
// max length should be 56ish
func formatGifText(maxLineLength int, lines []string) string {
	text := []string{}
	for _, line := range lines {
		currentLine := []string{}
		for _, word := range strings.Split(line, " ") {
			if lineLength(currentLine)+(len(word)+1) > maxLineLength {
				text = append(text, strings.Join(currentLine, " "))
				currentLine = []string{word}
				continue
			}
			currentLine = append(currentLine, word)
		}
		if len(currentLine) > 0 {
			text = append(text, strings.Join(currentLine, " "))
		}
	}

	finalText := strings.Join(text, "\n")
	finalText = strings.Replace(finalText, "'", "’", -1)
	finalText = strings.Replace(finalText, ":", `\:`, -1)
	return strings.TrimSpace(finalText)
}

func lineLength(line []string) int {
	if len(line) == 0 {
		return 0
	}
	total := 0
	for _, v := range line {
		total += len(v)
	}
	// total + number of spaces that would be in the line
	return total + (len(line) - 1)
}

type customIdPayload struct {
	Publication   string
	Series        int32
	Episode       int32
	StartPosition int64
	EndPosition   int64
	ExtendOrTrim  time.Duration
	Shift         time.Duration
}

func (c *customIdPayload) DialogID() string {
	return fmt.Sprintf("%s-%s", c.EpisodeID(), c.PositionRange())
}

func (c *customIdPayload) EpisodeID() string {
	return fmt.Sprintf("%s-%s", c.Publication, util.FormatSeriesAndEpisode(int(c.Series), int(c.Episode)))
}

func (c *customIdPayload) PositionRange() string {
	position := fmt.Sprintf("%d", c.StartPosition)
	if c.EndPosition > c.StartPosition {
		position = fmt.Sprintf("%s_%d", position, c.EndPosition)
	}
	return position
}

func (c *customIdPayload) String() string {
	return fmt.Sprintf(
		"%s-%s-%s%s%s",
		c.Publication,
		util.FormatSeriesAndEpisode(int(c.Series), int(c.Episode)),
		c.PositionRange(),
		fmt.Sprintf(":%s", c.ExtendOrTrim.String()),
		fmt.Sprintf(":%s", c.Shift.String()),
	)
}

func (c *customIdPayload) WithShift(shift time.Duration) *customIdPayload {
	cp := *c
	cp.Shift = shift
	return &cp
}

func (c *customIdPayload) WithExtend(extendOrTrim time.Duration) *customIdPayload {
	cp := *c
	cp.ExtendOrTrim = extendOrTrim
	return &cp
}

func (c *customIdPayload) WithStartPosition(start int64) *customIdPayload {
	cp := *c
	cp.StartPosition = start
	return &cp
}
func (c *customIdPayload) WithEndPosition(end int64) *customIdPayload {
	cp := *c
	cp.EndPosition = end
	return &cp
}

// e.g. peepshow-S08E06-1[_4][:1s:1s]
func parseCustomIDPayload(payloadStr string) (*customIdPayload, error) {
	parts := strings.SplitN(payloadStr, "-", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("unrecognized payload format: %s", payloadStr)
	}
	payload := &customIdPayload{
		Publication: parts[0],
	}
	var err error
	payload.Series, payload.Episode, err = util.ExtractSeriesAndEpisode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("unrecognozied episode format: %w", err)
	}
	posParts := strings.Split(parts[2], ":")
	if len(posParts) > 0 {
		var startAndEnd []string
		if strings.Contains(posParts[0], "_") {
			startAndEnd = strings.SplitN(posParts[0], "_", 2)
		} else {
			startAndEnd = []string{posParts[0], posParts[0]}
		}
		startPosition, err := strconv.Atoi(startAndEnd[0])
		if err != nil {
			return nil, fmt.Errorf("unrecognized start position: %s (%s)", startAndEnd[0], payloadStr)
		}
		payload.StartPosition = int64(startPosition)

		endPosition, err := strconv.Atoi(startAndEnd[1])
		if err != nil {
			return nil, fmt.Errorf("unrecognized end position: %s (%s)", startAndEnd[1], payloadStr)
		}
		payload.EndPosition = max(int64(endPosition), payload.StartPosition)
	}
	if len(posParts) > 1 {
		trimOrExtend, err := time.ParseDuration(posParts[1])
		if err != nil {
			return nil, fmt.Errorf("unrecognized trim/extend format: %s", posParts[1])
		}
		payload.ExtendOrTrim = trimOrExtend
	}
	if len(posParts) > 2 {
		shift, err := time.ParseDuration(posParts[2])
		if err != nil {
			return nil, fmt.Errorf("unrecognized shift format: %s", posParts[2])
		}
		payload.Shift = shift
	}
	return payload, nil
}

func uniqueUser(m *discordgo.Member) string {
	return m.DisplayName() + " (" + shortID(m.User.ID) + ")"
}

func shortID(longID string) string {
	if len(longID) < 6 {
		return longID
	}
	return longID[len(longID)-6:]
}

func createDrawtextFilter(dialog []model2.Dialog, customText []string) string {
	drawTextCommands := []string{}
	timestampOffsets := dialog[0].StartTimestamp
	for k, line := range dialog {
		dialogText := line.Content
		if len(customText) > k {
			dialogText = customText[k]
		}
		startSecond := line.StartTimestamp - timestampOffsets
		endSecond := line.EndTimestamp - timestampOffsets
		drawTextCommands = append(drawTextCommands, fmt.Sprintf(
			`drawtext=text='%s':fontcolor=white:fontsize=16:box=1:boxcolor=black@0.5:boxborderw=5:x=(w-text_w)/2:y=(h-(text_h+10)):enable='between(t,%0.2f,%0.2f)'`,
			formatGifText(56, strings.Split(dialogText, "\n")),
			startSecond.Seconds(),
			endSecond.Seconds(),
		))
	}
	return fmt.Sprintf("[0:v]%s", strings.Join(drawTextCommands, ", "))
}
