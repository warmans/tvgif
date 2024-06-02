package discord

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	ffmpeg_go "github.com/u2takey/ffmpeg-go"
	"github.com/warmans/tvgif/pkg/filter"
	"github.com/warmans/tvgif/pkg/limits"
	"github.com/warmans/tvgif/pkg/mediacache"
	"github.com/warmans/tvgif/pkg/search"
	"github.com/warmans/tvgif/pkg/search/model"
	"github.com/warmans/tvgif/pkg/searchterms"
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

var punctuation = regexp.MustCompile(`[^a-zA-Z0-9\s]+`)
var spaces = regexp.MustCompile(`[\s]{2,}`)
var metaWhitespace = regexp.MustCompile(`[\n\r\t]+`)

var rendersInProgress = map[string]string{}
var renderMutex = sync.RWMutex{}
var errRenderInProgress = errors.New("render in progress")
var errDuplicateInteraction = errors.New("interaction already processing")

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
) (*Bot, error) {

	publications, err := searcher.ListTerms(context.Background(), "publication")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch publications: %w", err)
	}
	publicationChoices := []*discordgo.ApplicationCommandOptionChoice{}
	for _, v := range publications {
		publicationChoices = append(publicationChoices, &discordgo.ApplicationCommandOptionChoice{
			Name:  v,
			Value: v,
		})
	}

	bot := &Bot{
		logger:     logger,
		session:    session,
		searcher:   searcher,
		mediaCache: mediaCache,
		mediaPath:  mediaPath,
		commands: []*discordgo.ApplicationCommand{
			{
				Name:        "tvgif",
				Description: "Search for a TV show gif",
				Type:        discordgo.ChatApplicationCommand,
				Options: []*discordgo.ApplicationCommandOption{
					{
						Name:         "query",
						Description:  `Enter a partial quote. Phrase match with "double quotes". Filter with ~publication and #s1e01. `,
						Type:         discordgo.ApplicationCommandOptionString,
						Required:     true,
						Autocomplete: true,
					},
					{
						Type:              discordgo.ApplicationCommandOptionString,
						Name:              "publication",
						NameLocalizations: nil,
						Description:       "Filter by publication",
						Required:          false,
						Autocomplete:      false,
						Choices:           publicationChoices,
						MinValue:          nil,
						MaxValue:          0,
						MinLength:         nil,
						MaxLength:         0,
					},
				},
			},
		},
	}
	bot.commandHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"tvgif": bot.queryBegin,
	}
	bot.buttonHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate, suffix string){
		"tvgif_confirm": bot.queryComplete,
		"tvgif_custom":  bot.editModal,
		"tgif_update":   bot.updatePreview,
	}
	bot.modalHandlers = map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate, suffix string){
		"tvgif_confirm": bot.queryCompleteCustom,
	}

	return bot, nil
}

type Bot struct {
	logger          *slog.Logger
	session         *discordgo.Session
	searcher        search.Searcher
	mediaCache      *mediacache.Cache
	mediaPath       string
	commands        []*discordgo.ApplicationCommand
	commandHandlers map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate)
	buttonHandlers  map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate, customIdPayload string)
	modalHandlers   map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate, customIdPayload string)
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
			username = i.Member.DisplayName()
		}
		customID, err := parseCustomIDPayload(selection)
		if err != nil {
			b.respondError(s, i, fmt.Errorf("invalid selection"))
			return
		}
		res, err := b.searcher.Get(context.Background(), selection)
		if err != nil {
			b.logger.Error("failed to fetch dialog", slog.String("err", err.Error()))
			b.respondError(s, i, fmt.Errorf("failed to fetch selected line"))
			return
		}
		if err := b.createVideoPreview(s, i, *res, username, customID); err != nil {
			b.logger.Error("Failed to begin video response", slog.String("err", err.Error()))
		}
		return
	case discordgo.InteractionApplicationCommandAutocomplete:
		data := i.ApplicationCommandData()

		rawTerms := strings.TrimSpace(data.Options[0].StringValue())

		publication := ""
		if len(data.Options) > 1 {
			publication = strings.TrimSpace(data.Options[1].StringValue())
		}

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
		if publication != "" {
			f = filter.And(filter.Eq("publication", filter.String(publication)), f)
		}
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
			if publication != "" {
				name = fmt.Sprintf("[%s] %s", v.ShortEpisodeID(), v.Content)
			}
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
		username = i.Member.DisplayName()
	}

	customID, err := parseCustomIDPayload(customIDPayload)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to parse customID: %w", err))
		return
	}

	dialog, err := b.searcher.Get(context.Background(), customID.DialogID())
	if err != nil {
		b.logger.Error("failed to fetch dialog", slog.String("err", err.Error()), slog.String("id", customIDPayload))
		b.respondError(s, i, fmt.Errorf("failed to fetch selected line"))
		return
	}

	interactionResponse, err := b.buildGifResponse(*dialog, username, true, nil, customID)
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
		interactionResponse, err = b.buildGifResponse(*dialog, username, false, nil, customID)
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

func (b *Bot) createVideoPreview(
	s *discordgo.Session,
	i *discordgo.InteractionCreate,
	dialog model.DialogDocument,
	username string,
	customID *customIdPayload,
) error {
	// send a placeholder
	interactionResponse, err := b.buildGifResponse(dialog, username, true, nil, customID)
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
		interactionResponse, err = b.buildGifResponse(dialog, username, false, nil, customID)
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

		buttons, err := b.createButtons(&dialog, customID)
		if err != nil {
			b.logger.Error("edit failed. Failed to create buttons", slog.String("err", err.Error()))
			return
		}

		b.logger.Info("Completed rendering")
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
	dialog, err := b.searcher.Get(context.Background(), customID.DialogID())
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to fetch original dialog"))
		return
	}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: encodeCustomID("tvgif_confirm", customIDPayload),
			Title:    "Edit and Post GIF (no preview)",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:  "custom_text",
							Label:     "Gif Text",
							Style:     discordgo.TextInputParagraph,
							Required:  false,
							MaxLength: 128,
							Value:     dialog.Content,
						},
					},
				},
			},
		},
	})
	if err != nil {
		b.respondError(s, i, err)
	}
}

func (b *Bot) createButtons(dialog *model.DialogDocument, customID *customIdPayload) ([]discordgo.MessageComponent, error) {
	dialogDuration, err := dialog.Duration()
	if err != nil {
		return nil, err
	}
	buttons := []discordgo.MessageComponent{
		discordgo.Button{
			// Label is what the user will see on the button.
			Label: "Shift 1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏪",
			},
			// Style provides coloring of the button. There are not so many styles tho.
			Style: discordgo.SecondaryButton,
			// Disabled allows bot to disable some buttons for users.
			Disabled: false,
			// CustomID is a thing telling Discord which data to send when this button will be pressed.
			CustomID: encodeCustomID("tgif_update", idWithExtendOrShift(dialog.ID, customID.ExtendOrTrim, customID.Shift+(0-time.Second))),
		},
		discordgo.Button{
			// Label is what the user will see on the button.
			Label: "Shift 1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			// Style provides coloring of the button. There are not so many styles tho.
			Style: discordgo.SecondaryButton,
			// Disabled allows bot to disable some buttons for users.
			Disabled: false,
			// CustomID is a thing telling Discord which data to send when this button will be pressed.
			CustomID: encodeCustomID("tgif_update", idWithExtendOrShift(dialog.ID, customID.ExtendOrTrim, customID.Shift+time.Second)),
		},
	}
	if (dialogDuration+customID.ExtendOrTrim)-time.Second > 0 {
		buttons = append(buttons, discordgo.Button{
			// Label is what the user will see on the button.
			Label: "Trim 1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "✂",
			},
			// Style provides coloring of the button. There are not so many styles tho.
			Style: discordgo.SecondaryButton,
			// Disabled allows bot to disable some buttons for users.
			Disabled: false,
			// CustomID is a thing telling Discord which data to send when this button will be pressed.
			CustomID: encodeCustomID("tgif_update", idWithExtendOrShift(dialog.ID, customID.ExtendOrTrim-time.Second, customID.Shift)),
		})
	}
	if (dialogDuration+customID.ExtendOrTrim)+time.Second <= limits.MaxGifDuration {
		buttons = append(buttons, discordgo.Button{
			// Label is what the user will see on the button.
			Label: "Extend 1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "➕",
			},
			// Style provides coloring of the button. There are not so many styles tho.
			Style: discordgo.SecondaryButton,
			// Disabled allows bot to disable some buttons for users.
			Disabled: false,
			// CustomID is a thing telling Discord which data to send when this button will be pressed.
			CustomID: encodeCustomID("tgif_update", idWithExtendOrShift(dialog.ID, customID.ExtendOrTrim+time.Second, customID.Shift)),
		})
	}
	return []discordgo.MessageComponent{
		// ActionRow is a container of all buttons within the same row.
		discordgo.ActionsRow{
			Components: buttons,
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					// Label is what the user will see on the button.
					Label: "Post",
					Emoji: &discordgo.ComponentEmoji{
						Name: "✅",
					},
					// Style provides coloring of the button. There are not so many styles tho.
					Style: discordgo.PrimaryButton,
					// Disabled allows bot to disable some buttons for users.
					Disabled: false,
					// CustomID is a thing telling Discord which data to send when this button will be pressed.
					CustomID: encodeCustomID("tvgif_confirm", idWithExtendOrShift(dialog.ID, customID.ExtendOrTrim, customID.Shift)),
				},
				discordgo.Button{
					// Label is what the user will see on the button.
					Label: "Customize Text",
					Emoji: &discordgo.ComponentEmoji{
						Name: "🔧",
					},
					// Style provides coloring of the button. There are not so many styles tho.
					Style: discordgo.SecondaryButton,
					// Disabled allows bot to disable some buttons for users.
					Disabled: false,
					// CustomID is a thing telling Discord which data to send when this button will be pressed.
					CustomID: encodeCustomID("tvgif_custom", idWithExtendOrShift(dialog.ID, customID.ExtendOrTrim, customID.Shift)),
				},
			},
		},
	}, nil
}

func (b *Bot) queryCompleteCustom(s *discordgo.Session, i *discordgo.InteractionCreate, customIDPayload string) {
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
		username = i.Member.DisplayName()
	}
	dialog, err := b.searcher.Get(context.Background(), customID.DialogID())
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to fetch selected line"))
		return
	}
	customText := i.Interaction.ModalSubmitData().Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value

	if err = b.completeVideoResponse(s, i, *dialog, username, util.ToPtr(customText), customID); err != nil {
		b.respondError(s, i, err)
	}
}

func (b *Bot) queryComplete(s *discordgo.Session, i *discordgo.InteractionCreate, customIDPayload string) {

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
		username = i.Member.DisplayName()
	}
	dialog, err := b.searcher.Get(context.Background(), customID.DialogID())
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to fetch selected line"))
		return
	}
	if err := b.completeVideoResponse(s, i, *dialog, username, nil, customID); err != nil {
		b.logger.Error("Failed to complete video response", slog.String("err", err.Error()))
	}
}

func (b *Bot) completeVideoResponse(s *discordgo.Session, i *discordgo.InteractionCreate, dialog model.DialogDocument, username string, customText *string, customID *customIdPayload) error {

	interactionResponse, err := b.buildGifResponse(dialog, username, true, nil, customID)
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
		interactionResponse, err = b.buildGifResponse(dialog, username, false, customText, customID)
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

func (b *Bot) buildGifResponse(dialog model.DialogDocument, username string, placeholder bool, customText *string, customID *customIdPayload) (*discordgo.InteractionResponse, error) {
	cleanup, err := lockRenderer(username, dialog.ID)
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
	if !placeholder {
		gif, err := b.renderGif(dialog, customText, customID)
		if err != nil {
			return nil, err
		}
		files = []*discordgo.File{gif}
		bodyText = ""
	} else {
		bodyText = ":timer: Rendering gif..."
	}
	editLabel := ""
	if customText != nil {
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
				"%s\n\n`%s@%s-%s%s%s`%s | Posted by %s",
				bodyText,
				dialog.ID,
				dialog.StartTimestamp,
				dialog.EndTimestamp,
				shiftLabel,
				extendLabel,
				editLabel,
				username,
			),
			Files:       files,
			Attachments: util.ToPtr([]*discordgo.MessageAttachment{}),
		},
	}, nil
}

func (b *Bot) respondError(s *discordgo.Session, i *discordgo.InteractionCreate, err error) {
	b.logger.Error("Error response was sent", slog.String("err", err.Error()))
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

func (b *Bot) renderGif(dialog model.DialogDocument, customText *string, customID *customIdPayload) (*discordgo.File, error) {

	disableCaching := customID.ExtendOrTrim != 0 || customID.Shift != 0
	dialogText := strings.Split(dialog.Content, "\n")
	if customText != nil {
		disableCaching = true
		if *customText == "" {
			dialogText = []string{}
		} else {
			dialogText = strings.Split(*customText, "\n")
		}
	}

	startTimestamp, err := time.ParseDuration(dialog.StartTimestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse start time: %w", err)
	}
	endTimestamp, err := time.ParseDuration(dialog.EndTimestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse start time: %w", err)
	}
	if customID.Shift != 0 {
		startTimestamp += customID.Shift
		endTimestamp += customID.Shift
	}
	if customID.ExtendOrTrim != 0 {
		endTimestamp = endTimestamp + customID.ExtendOrTrim
		if endTimestamp <= startTimestamp {
			endTimestamp = startTimestamp + time.Second
		}
	}
	if endTimestamp-startTimestamp > limits.MaxGifDuration {
		endTimestamp = startTimestamp + limits.MaxGifDuration
	}
	b.logger.Debug(
		"Exporting gif",
		slog.Duration("start", startTimestamp),
		slog.Duration("end", endTimestamp),
		slog.String("custom_text", strings.Join(dialogText, " ")),
	)

	startTime := time.Now()
	buff := &bytes.Buffer{}

	cacheHit, err := b.mediaCache.Get(dialog.ID, buff, disableCaching, func(writer io.Writer) error {

		//todo: write content type headers?

		err := ffmpeg_go.
			Input(path.Join(b.mediaPath, dialog.VideoFileName),
				ffmpeg_go.KwArgs{
					"ss": fmt.Sprintf("%0.2f", startTimestamp.Seconds()),
					"to": fmt.Sprintf("%0.2f", endTimestamp.Seconds()),
				}).
			Output("pipe:",
				ffmpeg_go.KwArgs{
					"format": "gif",
					"filter_complex": fmt.Sprintf(
						"[0:v]drawtext=text='%s':fontcolor=white:fontsize=16:box=1:boxcolor=black@0.5:boxborderw=5:x=(w-text_w)/2:y=(h-(text_h+10))",
						formatGifText(56, dialogText),
					),
				},
			).WithOutput(writer, os.Stderr).Run()
		if err != nil {
			b.logger.Error("ffmpeg failed", slog.String("err", err.Error()))
			return err
		}

		return nil
	})
	if err != nil {
		b.logger.Error("cache fetch failed", slog.String("err", err.Error()))
		return nil, err
	}
	if cacheHit {
		b.logger.Info("Cache hit", slog.Duration("time taken", time.Since(startTime)), slog.String("cache_key", dialog.ID))
	} else {
		b.logger.Info("Cache miss", slog.Duration("time taken", time.Since(startTime)), slog.String("cache_key", dialog.ID))
	}

	return &discordgo.File{
		Name:        createFileName(dialog, "gif"),
		ContentType: "image/gif",
		Reader:      buff,
	}, nil
}

func createFileName(dialog model.DialogDocument, suffix string) string {
	if contentFilename := contentToFilename(dialog.Content); contentFilename != "" {
		return fmt.Sprintf("%s.%s", contentFilename, suffix)
	}
	return fmt.Sprintf("%s.%s", dialog.ID, suffix)
}

func contentToFilename(rawContent string) string {
	rawContent = punctuation.ReplaceAllString(rawContent, "")
	rawContent = spaces.ReplaceAllString(rawContent, " ")
	rawContent = metaWhitespace.ReplaceAllString(rawContent, " ")
	rawContent = strings.ToLower(strings.TrimSpace(rawContent))
	split := strings.Split(rawContent, " ")
	if len(split) > 9 {
		split = split[:8]
	}
	return strings.Join(split, "-")
}

func encodeCustomID(action string, dialogID string) string {
	return fmt.Sprintf("%s:%s", action, dialogID)
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

func idWithExtendOrShift(id string, extend time.Duration, shift time.Duration) string {
	return fmt.Sprintf("%s:%s:%s", id, extend.String(), shift.String())
}

type customIdPayload struct {
	Publication  string
	Series       int32
	Episode      int32
	Position     int64
	ExtendOrTrim time.Duration
	Shift        time.Duration
}

func (c *customIdPayload) DialogID() string {
	return fmt.Sprintf("%s-%s-%d", c.Publication, util.FormatSeriesAndEpisode(int(c.Series), int(c.Episode)), c.Position)
}

func (c *customIdPayload) String() string {
	return fmt.Sprintf(
		"%s-%s-%d%s%s",
		c.Publication,
		util.FormatSeriesAndEpisode(int(c.Series), int(c.Episode)),
		c.Position,
		fmt.Sprintf(":%s", c.ExtendOrTrim.String()),
		fmt.Sprintf(":%s", c.Shift.String()),
	)
}

// e.g. peepshow-S08E06-1[:1s:1s]
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
		position, err := strconv.Atoi(posParts[0])
		if err != nil {
			return nil, fmt.Errorf("unrecognized position: %s", parts[2])
		}
		payload.Position = int64(position)
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
