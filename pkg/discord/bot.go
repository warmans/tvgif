package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	ffmpeg_go "github.com/u2takey/ffmpeg-go"
	"github.com/warmans/tvgif/pkg/docs"
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
	"strings"
	"sync"
	"time"
)

type Command string

const (
	CommandSearch Command = "tvgif"
	CommandHelp   Command = "tvgif-help"
	CommandDelete Command = "tvgif-delete"
)

type Action string

const (
	ActionConfirmPostGif      = Action("cfrmg")
	ActionOpenCustomTextModal = Action("cstm")
	ActionNextResult          = Action("nxt")
	ActionPrevResult          = Action("prv")
	ActionUpdatePreview       = Action("upd")
)

type OutputFileType string

const (
	OutputGif  = OutputFileType("gif")
	OutputWebm = OutputFileType("webm")
)

var postedByUser = regexp.MustCompile(`.+ posted by \x60([^\x60]+)\x60`)
var extractOriginalTerms = regexp.MustCompile(".*original terms: `([^`]+)`")

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
	originalTerms  string
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

func withOriginalTerms(terms string) responseOption {
	return func(options *responseOptions) {
		options.originalTerms = terms
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
	docsRepo *docs.Repo,
) (*Bot, error) {

	docsTopics := []*discordgo.ApplicationCommandOptionChoice{
		{Name: "List Publications", Value: "publications"},
	}
	for _, v := range docsRepo.Topics() {
		docsTopics = append(docsTopics, &discordgo.ApplicationCommandOptionChoice{Name: v, Value: v})
	}

	bot := &Bot{
		logger:      logger,
		session:     session,
		searcher:    searcher,
		mediaCache:  mediaCache,
		mediaPath:   mediaPath,
		srtStore:    srtStore,
		botUsername: botUsername,
		docs:        docsRepo,
		commands: []*discordgo.ApplicationCommand{
			{
				Name:        string(CommandSearch),
				Description: "Search for a TV show gif",
				Type:        discordgo.ChatApplicationCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{
						Name:         "query",
						Description:  `Phrase match with "quotes". Page with >N e.g. >10. Filter with ~publication #s1e01 +10m30s`,
						Type:         discordgo.ApplicationCommandOptionString,
						Required:     true,
						Autocomplete: true,
					},
				},
			},
			{
				Name:        string(CommandHelp),
				Description: "Show tvgif information",
				Type:        discordgo.ChatApplicationCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{
						Name:         "topic",
						Description:  "Help topic",
						Type:         discordgo.ApplicationCommandOptionString,
						Required:     true,
						Autocomplete: false,
						Choices:      docsTopics,
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
		string(CommandHelp):   bot.helpText,
		string(CommandDelete): bot.deletePost,
	}
	bot.buttonHandlers = map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, suffix string){
		ActionConfirmPostGif:      bot.postGif,
		ActionNextResult:          bot.nextResult,
		ActionPrevResult:          bot.previousResult,
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
	docs            *docs.Repo
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
	if results[1] != uniqueUser(i.Member, i.User) {
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

		result := &struct {
			Terms string
			ID    string
		}{}
		err := json.Unmarshal([]byte(selection), result)
		if err != nil {
			b.respondError(s, i, fmt.Errorf("failed to marshal result ID: %w", err))
			return
		}

		username := uniqueUser(i.Member, i.User)
		customID, err := parseCustomIDPayload(result.ID)
		if err != nil {
			b.respondError(s, i, fmt.Errorf("invalid selection: %s", result.ID))
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
		b.logger.Info("Creating...", slog.String("custom_id", customID.String()))
		if err := b.createGifPreview(s, i, dialog, username, customID, result.Terms); err != nil {
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

		res, err := b.searcher.Search(context.Background(), terms)
		if err != nil {
			b.logger.Error("Failed to fetch autocomplete options", slog.String("err", err.Error()))
			return
		}
		var choices []*discordgo.ApplicationCommandOptionChoice
		for _, v := range res {
			payload, err := json.Marshal(struct {
				Terms string
				ID    string
			}{rawTerms, v.ID})
			if err != nil {
				b.logger.Error("failed to marshal result", slog.String("err", err.Error()))
				continue
			}
			name := fmt.Sprintf("[%s] %s", v.EpisodeID, v.Content)
			choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
				Name:  util.TrimToN(name, 100),
				Value: string(payload),
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
	b.logger.Info("Editing...", slog.String("custom_id", customIDPayload))
	username := uniqueUser(i.Member, i.User)

	terms := "unknown"
	foundTerms := extractOriginalTerms.FindStringSubmatch(i.Message.Content)
	if len(foundTerms) == 2 {
		terms = foundTerms[1]
	}

	customID, err := parseCustomIDPayload(customIDPayload)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to parse customID (%s): %w", customIDPayload, err))
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

	interactionResponse, err := b.buildInteractionResponse(dialog, customID, withUsername(username), withPlaceholder(), withOriginalTerms(terms))
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
		interactionResponse, err = b.buildInteractionResponse(dialog, customID, withUsername(username), withOriginalTerms(terms))
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
	originalTerms string,
) error {
	// send a placeholder
	interactionResponse, err := b.buildInteractionResponse(dialog, customID, withUsername(username), withPlaceholder(), withOriginalTerms(originalTerms))
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
		interactionResponse, err = b.buildInteractionResponse(
			dialog,
			customID,
			withUsername(username),
			withOriginalTerms(originalTerms),
		)
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

	dialogDuration := (dialog[len(dialog)-1].EndTimestamp - dialog[0].StartTimestamp) + customID.Opts.ExtendOrTrim

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
		//		CustomID: encodeAction(ActionUpdatePreview, customID.WithStartPosition(prevcustomID.Opts.StartPosition)),
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
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Opts.Shift+(0-(time.Second*5)))),
		},
		discordgo.Button{
			Label: "1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏪",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Opts.Shift+(0-time.Second))),
		},
		discordgo.Button{
			Label: "0.5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Opts.Shift+(time.Second/2))),
		},
		discordgo.Button{
			Label: "1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Opts.Shift+time.Second)),
		},
		discordgo.Button{
			Label: "5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithShift(customID.Opts.Shift+(time.Second*5))),
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
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.Opts.ExtendOrTrim+(time.Second/2))),
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
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.Opts.ExtendOrTrim+time.Second)),
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
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.Opts.ExtendOrTrim+(time.Second*5))),
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
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.Opts.ExtendOrTrim+(time.Second*10))),
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
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.Opts.ExtendOrTrim-(time.Second/2))),
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
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.Opts.ExtendOrTrim-time.Second)),
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
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.Opts.ExtendOrTrim-(time.Second*5))),
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

	//const panIncrement = 75
	//stickerButtons := []discordgo.MessageComponent{}
	//if customID.Opts.Sticker != nil {
	//	if customID.Opts.Sticker.X+panIncrement <= 596 {
	//		stickerButtons = append(stickerButtons, discordgo.Button{
	//			Label: fmt.Sprintf("%dpx", panIncrement),
	//			Emoji: &discordgo.ComponentEmoji{
	//				Name: "➡",
	//			},
	//			Style:    discordgo.SecondaryButton,
	//			Disabled: false,
	//			CustomID: encodeAction(ActionUpdatePreview, customID.WithStickerXIncrement(panIncrement)),
	//		})
	//	}
	//	if customID.Opts.Sticker.X-panIncrement >= 0 {
	//		stickerButtons = append(stickerButtons, discordgo.Button{
	//			Label: fmt.Sprintf("%dpx", panIncrement),
	//			Emoji: &discordgo.ComponentEmoji{
	//				Name: "⬅",
	//			},
	//			Style:    discordgo.SecondaryButton,
	//			Disabled: false,
	//			CustomID: encodeAction(ActionUpdatePreview, customID.WithStickerXIncrement(0-panIncrement)),
	//		})
	//	}
	//	if customID.Opts.Sticker.Y+panIncrement <= 336 {
	//		stickerButtons = append(stickerButtons, discordgo.Button{
	//			Label: fmt.Sprintf("%dpx", panIncrement),
	//			Emoji: &discordgo.ComponentEmoji{
	//				Name: "⬇",
	//			},
	//			Style:    discordgo.SecondaryButton,
	//			Disabled: false,
	//			CustomID: encodeAction(ActionUpdatePreview, customID.WithStickerYIncrement(panIncrement)),
	//		})
	//	}
	//	if customID.Opts.Sticker.Y-panIncrement >= 0 {
	//		stickerButtons = append(stickerButtons, discordgo.Button{
	//			Label: fmt.Sprintf("%dpx", panIncrement),
	//			Emoji: &discordgo.ComponentEmoji{
	//				Name: "⬆",
	//			},
	//			Style:    discordgo.SecondaryButton,
	//			Disabled: false,
	//			CustomID: encodeAction(ActionUpdatePreview, customID.WithStickerYIncrement(0-panIncrement)),
	//		})
	//	}
	//}

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
	//if len(stickerButtons) > 0 {
	//	actions = append(actions, discordgo.ActionsRow{Components: stickerButtons})
	//}
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
			discordgo.Button{
				// Label is what the user will see on the button.
				Label: ifOr(customID.Opts.Sticker == nil, "Stickerfy", "Unstickerfy"),
				Emoji: &discordgo.ComponentEmoji{
					Name: "🖼",
				},
				// Style provides coloring of the button. There are not so many styles tho.
				Style: discordgo.SecondaryButton,
				// Disabled allows bot to disable some buttons for users.
				Disabled: false,
				// CustomID is a thing telling Discord which data to send when this button will be pressed.
				CustomID: encodeAction(ActionUpdatePreview, customID.WithToggleStickerMode()),
			},
			discordgo.Button{
				// Label is what the user will see on the button.
				Label: "Prev",
				Emoji: &discordgo.ComponentEmoji{
					Name: "❌",
				},
				// Style provides coloring of the button. There are not so many styles tho.
				Style: discordgo.SecondaryButton,
				// Disabled allows bot to disable some buttons for users.
				Disabled: false,
				// CustomID is a thing telling Discord which data to send when this button will be pressed.
				CustomID: encodeAction(ActionPrevResult, customID),
			},
			discordgo.Button{
				// Label is what the user will see on the button.
				Label: "Next",
				Emoji: &discordgo.ComponentEmoji{
					Name: "❌",
				},
				// Style provides coloring of the button. There are not so many styles tho.
				Style: discordgo.SecondaryButton,
				// Disabled allows bot to disable some buttons for users.
				Disabled: false,
				// CustomID is a thing telling Discord which data to send when this button will be pressed.
				CustomID: encodeAction(ActionNextResult, customID),
			},
		},
	})

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

	username := uniqueUser(i.Member, i.User)

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
	username := uniqueUser(i.Member, i.User)
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
			b.logger.Warn("Ignored duplicated interaction")
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
	if customID.Opts.ExtendOrTrim != 0 {
		if customID.Opts.ExtendOrTrim > 0 {
			extendLabel = fmt.Sprintf("(+%s)", customID.Opts.ExtendOrTrim.String())
		} else {
			extendLabel = fmt.Sprintf("(%s)", customID.Opts.ExtendOrTrim.String())
		}
	}
	shiftLabel := ""
	if customID.Opts.Shift != 0 {
		if customID.Opts.Shift > 0 {
			shiftLabel = fmt.Sprintf("(>>%s)", customID.Opts.Shift.String())
		} else {
			shiftLabel = fmt.Sprintf("(<<%s)", customID.Opts.Shift.String())
		}
	}
	stickerLabel := ""
	if customID.Opts.Sticker != nil {
		stickerLabel = "(sticker)"
	}
	originalTerms := ""
	if opts.originalTerms != "" {
		originalTerms = fmt.Sprintf("\noriginal terms: `%s`", opts.originalTerms)
	}
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf(
				"%s\n\n`%s@%s-%s%s%s%s%s` posted by `%s`%s",
				bodyText,
				customID.DialogID(),
				dialog[0].StartTimestamp,
				dialog[len(dialog)-1].EndTimestamp,
				shiftLabel,
				extendLabel,
				editLabel,
				stickerLabel,
				opts.username,
				originalTerms,
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
	disableCaching := customID.Opts.ExtendOrTrim != 0 || customID.Opts.Shift != 0 || customText != nil || customID.Opts.Sticker != nil

	startTimestamp := dialog[0].StartTimestamp
	endTimestamp := dialog[len(dialog)-1].EndTimestamp

	if customID.Opts.Shift != 0 {
		startTimestamp += customID.Opts.Shift
		endTimestamp += customID.Opts.Shift
	}
	if customID.Opts.ExtendOrTrim != 0 {
		endTimestamp += customID.Opts.ExtendOrTrim
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
						"filter_complex": joinFilters(createDrawtextFilter(dialog, customText, customID.Opts), createCropFilter(customID.Opts), createResizeFilter(customID.Opts)),
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

func (b *Bot) helpText(s *discordgo.Session, i *discordgo.InteractionCreate) {
	topic := i.ApplicationCommandData().Options[0].StringValue()
	if topic == "" {
		b.respondError(s, i, fmt.Errorf("topic was not selected"))
		return
	}

	var resp *discordgo.InteractionResponseData
	switch topic {
	case "publications":
		publications, err := b.srtStore.ListPublications()
		if err != nil {
			b.respondError(s, i, err)
			return
		}
		sb := &strings.Builder{}
		sb.WriteString("Available Publications: \n")
		for _, v := range publications {
			if _, err := fmt.Fprintf(sb, "* `%s` - `S[%s]`\n", v.Name, strings.Join(v.Series, ", ")); err != nil {
				b.respondError(s, i, err)
				return
			}
		}
		resp = &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: sb.String(),
		}
	default:
		data, err := b.docs.Get(topic)
		if err != nil {
			b.respondError(s, i, err)
			return
		}
		resp = &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: data,
		}
	}

	err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: resp,
	})
	if err != nil {
		b.respondError(s, i, err)
		return
	}
}

func (b *Bot) nextResult(s *discordgo.Session, i *discordgo.InteractionCreate, customIDPayload string) {
	b.nextOrPreviousResult(s, i, customIDPayload, true)
}

func (b *Bot) previousResult(s *discordgo.Session, i *discordgo.InteractionCreate, customIDPayload string) {
	b.nextOrPreviousResult(s, i, customIDPayload, false)
}

func (b *Bot) nextOrPreviousResult(s *discordgo.Session, i *discordgo.InteractionCreate, customIDPayload string, next bool) {

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
	foundTerms := extractOriginalTerms.FindStringSubmatch(i.Message.Content)
	if len(foundTerms) != 2 {
		b.respondError(s, i, fmt.Errorf("failed to extract terms from message"))
		return
	}
	terms, err := searchterms.Parse(foundTerms[1])
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to parse terms from message"))
		return
	}
	res, err := b.searcher.Search(context.Background(), terms, search.OverridePageSize(100))
	if err != nil {
		b.logger.Error("Failed to fetch autocomplete options", slog.String("err", err.Error()))
		return
	}
	currentSelection := -1
	for k, v := range res {
		if v.ID == customID.DialogID() {
			currentSelection = k
		}
	}
	var nextSelection int
	if next {
		nextSelection = currentSelection + 1
	} else {
		nextSelection = currentSelection - 1
	}
	// no more results or current result not found.
	if currentSelection == -1 || nextSelection >= len(res) || nextSelection < 0 {
		b.updatePreview(s, i, customIDPayload)
		return
	}

	b.updatePreview(s, i, res[nextSelection].ID)
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

func uniqueUser(m *discordgo.Member, u *discordgo.User) string {
	userName := "unknown"
	if m != nil {
		userName = m.DisplayName()
	}
	if userName == "" && u != nil {
		userName = u.Username
	}
	return userName + " (" + shortID(m.User.ID) + ")"
}

func shortID(longID string) string {
	if len(longID) < 6 {
		return longID
	}
	return longID[len(longID)-6:]
}

func createDrawtextFilter(dialog []model2.Dialog, customText []string, opts customIdOpts) string {
	if opts.Sticker != nil {
		return ""
	}
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
	return strings.Join(drawTextCommands, ", ")
}

func createCropFilter(opts customIdOpts) string {
	if opts.Sticker == nil {
		return ""
	}
	return "crop=w=336:h=336"
}

func createResizeFilter(opts customIdOpts) string {
	if opts.Sticker == nil {
		return ""
	}
	return "scale=160:160"
}

func joinFilters(filters ...string) string {
	out := ""
	filters = dropEmptyFilters(filters)
	for k, v := range filters {
		connector := ""
		if k < len(filters)-1 {
			connector = fmt.Sprintf("[f%d];[f%d]", k, k)
		}
		out += fmt.Sprintf("%s%s", v, connector)
	}
	return fmt.Sprintf("[0:v]%s", out)
}

func dropEmptyFilters(filters []string) []string {
	clean := []string{}
	for _, v := range filters {
		if v != "" {
			clean = append(clean, v)
		}
	}
	return clean
}

func positiveOrZero(val int32) int32 {
	if val < 0 {
		return 0
	}
	return val
}

func ifOr[T any](test bool, a T, b T) T {
	if test {
		return a
	}
	return b
}
