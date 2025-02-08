package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/warmans/tvgif/pkg/discord/customid"
	"github.com/warmans/tvgif/pkg/docs"
	"github.com/warmans/tvgif/pkg/limits"
	model2 "github.com/warmans/tvgif/pkg/model"
	"github.com/warmans/tvgif/pkg/render"
	"github.com/warmans/tvgif/pkg/search"
	"github.com/warmans/tvgif/pkg/searchterms"
	"github.com/warmans/tvgif/pkg/store"
	"github.com/warmans/tvgif/pkg/util"
	"log"
	"log/slog"
	"net/http"
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
	ActionConfirmPostGif       = Action("cfrmg")
	ActionConfirmPostCustomGif = Action("cnfmc")
	ActionConfirmPostWemb      = Action("cnfwb")
	ActionOpenCustomTextModal  = Action("cstm")
	ActionOpenCaptionModal     = Action("ctm")
	ActionNextResult           = Action("nxt")
	ActionPrevResult           = Action("prv")
	ActionUpdatePreview        = Action("upd")
	ActionUpdateState          = Action("sta")
	ActionSetCaption           = Action("sc")
)

var postedByUser = regexp.MustCompile(`.+ posted by \x60([^\x60]+)\x60`)
var extractState = regexp.MustCompile(`\|\|(\{.*\})\|\|`)

var rendersInProgress = map[string]string{}
var renderMutex = sync.RWMutex{}
var errRenderInProgress = errors.New("render in progress")
var errDuplicateInteraction = errors.New("interaction already processing")

func resolveResponseOptions(opts ...responseOption) *responseOptions {
	options := &responseOptions{
		username:       "unknown",
		outputFileType: customid.OutputGif,
	}
	for _, o := range opts {
		o(options)
	}
	return options
}

type responseOptions struct {
	username       string
	customText     []string
	outputFileType customid.OutputFileType
	placeholder    bool
	caption        string
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
			options.outputFileType = customid.OutputWebm
		} else {
			options.outputFileType = customid.OutputGif
		}
	}
}

func withPlaceholder() responseOption {
	return func(options *responseOptions) {
		options.placeholder = true
	}
}

func withCaption(caption string) responseOption {
	return func(options *responseOptions) {
		options.caption = caption
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

type OutputState struct {
	CustomID         *customid.Payload
	OriginalTerms    string  `json:"t,omitempty" `
	OriginalPosition *string `json:"p,omitempty"`
	Caption          string  `json:"c,omitempty"`
	DisableSubtitles bool    `json:"d,omitempty"`
}

func NewBot(
	logger *slog.Logger,
	session *discordgo.Session,
	searcher search.Searcher,
	renderer *render.Renderer,
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
		srtStore:    srtStore,
		botUsername: botUsername,
		docs:        docsRepo,
		renderer:    renderer,
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
		ActionConfirmPostGif:      bot.postGifFromPreview,
		ActionConfirmPostWemb:     bot.postWebm,
		ActionNextResult:          bot.nextResult,
		ActionPrevResult:          bot.previousResult,
		ActionOpenCustomTextModal: bot.editModal,
		ActionOpenCaptionModal:    bot.openCaptionModal,
		ActionUpdatePreview:       bot.updateCustomID,
		ActionUpdateState:         bot.updateState,
	}
	bot.modalHandlers = map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, suffix string){
		ActionConfirmPostCustomGif: bot.postCustomGif,
		ActionSetCaption:           bot.setCaption,
	}

	return bot, nil
}

type Bot struct {
	logger          *slog.Logger
	session         *discordgo.Session
	searcher        search.Searcher
	docs            *docs.Repo
	renderer        *render.Renderer
	srtStore        *store.SRTStore
	botUsername     string
	commands        []*discordgo.ApplicationCommand
	commandHandlers map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate)
	buttonHandlers  map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string)
	modalHandlers   map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string)
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
		customID, err := customid.ParsePayload(result.ID)
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

func (b *Bot) updateCustomID(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	b.updatePreviewWithOptions(s, i, &StateUpdate{Type: UpdateCustomID, Value: rawCustomID})
}

func (b *Bot) updatePreviewWithOptions(
	s *discordgo.Session,
	i *discordgo.InteractionCreate,
	upds ...*StateUpdate,
) {
	username := uniqueUser(i.Member, i.User)

	state, err := extractStateFromBody(i.Message.Content)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to get current state"))
		return
	}

	for _, upd := range upds {
		switch upd.Type {
		case ToggleSubs:
			state.DisableSubtitles = !state.DisableSubtitles
		case SetCaption:
			state.Caption = upd.Value
		case ResetCustomID:
			customID, err := customid.ParsePayload(upd.Value)
			if err != nil {
				b.respondError(s, i, fmt.Errorf("failed to parse customID (%s): %w", customID, err))
				return
			}
			// reset state completely when customID is reset
			state = &OutputState{
				CustomID: customID,
				// must keep this value to allow navigating between results
				OriginalTerms:    state.OriginalTerms,
				OriginalPosition: util.ToPtr(customID.PositionRange()),
			}
		case UpdateCustomID:
			customID, err := customid.ParsePayload(upd.Value)
			if err != nil {
				b.respondError(s, i, fmt.Errorf("failed to parse customID (%s): %w", customID, err))
				return
			}
			// just update the ID without resetting
			state.CustomID = customID
		}
	}

	dialog, err := b.srtStore.GetDialogRange(
		state.CustomID.Publication,
		state.CustomID.Series,
		state.CustomID.Episode,
		state.CustomID.StartPosition,
		state.CustomID.EndPosition,
	)
	if err != nil {
		b.respondError(
			s,
			i,
			fmt.Errorf("failed to fetch selected lines: %s", state.CustomID.String()),
			slog.String("err", err.Error()),
		)
		return
	}
	if len(dialog) == 0 {
		b.respondError(s, i, fmt.Errorf("no dialog was selected"))
		return
	}

	interactionResponse, err := b.buildInteractionResponse(
		dialog,
		state.CustomID,
		state,
		withUsername(username),
		withPlaceholder(),
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
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: interactionResponse.Data,
	}); err != nil {
		b.respondError(s, i, err)
		return
	}
	go func() {
		interactionResponse, err = b.buildInteractionResponse(
			dialog,
			state.CustomID,
			state,
			withUsername(username),
		)
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
		buttons, err := b.createButtons(dialog, state.CustomID)
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
	customID *customid.Payload,
	originalTerms string,
) error {
	state := &OutputState{
		CustomID:         customID,
		OriginalTerms:    originalTerms,
		OriginalPosition: util.ToPtr(customID.PositionRange()),
	}

	// send a placeholder
	interactionResponse, err := b.buildInteractionResponse(
		dialog,
		customID,
		state,
		withUsername(username),
		withPlaceholder(),
	)
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
			state,
			withUsername(username),
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

func (b *Bot) editModal(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	customID, err := customid.ParsePayload(rawCustomID)
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
			CustomID:   encodeAction(ActionConfirmPostCustomGif, customID),
			Title:      "Edit and Post (no preview)",
			Components: fields,
		},
	})
	if err != nil {
		b.respondError(s, i, err)
	}
}

func (b *Bot) openCaptionModal(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	customID, err := customid.ParsePayload(rawCustomID)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid customID"))
		return
	}

	state, err := extractStateFromBody(i.Message.Content)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to get current state"))
		return
	}

	fields := []discordgo.MessageComponent{discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.TextInput{
				CustomID:    "caption",
				Label:       "Caption",
				Style:       discordgo.TextInputParagraph,
				Required:    true,
				MaxLength:   128,
				Value:       state.Caption,
				Placeholder: "Caption added to top of image",
			},
		},
	}}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID:   encodeAction(ActionSetCaption, customID),
			Title:      "Set Caption",
			Components: fields,
		},
	})
	if err != nil {
		b.respondError(s, i, err)
	}
}

func (b *Bot) createButtons(dialog []model2.Dialog, customID *customid.Payload) ([]discordgo.MessageComponent, error) {

	before, after, err := b.srtStore.GetDialogContext(
		customID.Publication,
		customID.Series,
		customID.Episode,
		customID.StartPosition,
		customID.EndPosition,
	)
	if err != nil {
		return nil, err
	}

	dialogDuration := (dialog[len(dialog)-1].EndTimestamp - dialog[0].StartTimestamp) + customID.Opts.ExtendOrTrim

	navigateButtons := []discordgo.MessageComponent{}
	if len(before) > 0 {
		prevCustomID, err := customid.ParsePayload(before[0].ID(customID.EpisodeID()))
		if err != nil {
			return nil, err
		}
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
		nextCustomID, err := customid.ParsePayload(after[0].ID(customID.EpisodeID()))
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
	if dialogDuration+(time.Second/5) <= limits.MaxGifDuration {
		extendButtons = append(extendButtons, discordgo.Button{
			Label: "0.2s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "➕",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.Opts.ExtendOrTrim+(time.Second/5))),
		})
	}
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
	if dialogDuration-(time.Second/5) > 0 {
		trimButtons = append(trimButtons, discordgo.Button{
			Label: "0.2s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "✂",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithExtend(customID.Opts.ExtendOrTrim-(time.Second/5))),
		})
	}
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

	const panIncrementLarge = 50
	const panIncrementSmall = 25
	const widthIncrement = 50
	stickerButtons := []discordgo.MessageComponent{}
	if customID.Opts.Mode == customid.StickerMode {
		if customID.Opts.Sticker.X+panIncrementLarge <= 596 {
			stickerButtons = append(stickerButtons, discordgo.Button{
				Label: fmt.Sprintf("%dpx", panIncrementLarge),
				Emoji: &discordgo.ComponentEmoji{
					Name: "➡",
				},
				Style:    discordgo.SecondaryButton,
				Disabled: false,
				CustomID: encodeAction(ActionUpdatePreview, customID.WithStickerXIncrement(panIncrementLarge)),
			})
		}
		if customID.Opts.Sticker.X-panIncrementLarge >= 0 {
			stickerButtons = append(stickerButtons, discordgo.Button{
				Label: fmt.Sprintf("%dpx", panIncrementSmall),
				Emoji: &discordgo.ComponentEmoji{
					Name: "⬅",
				},
				Style:    discordgo.SecondaryButton,
				Disabled: false,
				CustomID: encodeAction(ActionUpdatePreview, customID.WithStickerXIncrement(0-panIncrementSmall)),
			})
		}
		if customID.Opts.Sticker.Y+panIncrementLarge <= 336 {
			stickerButtons = append(stickerButtons, discordgo.Button{
				Label: fmt.Sprintf("%dpx", panIncrementLarge),
				Emoji: &discordgo.ComponentEmoji{
					Name: "⬇",
				},
				Style:    discordgo.SecondaryButton,
				Disabled: false,
				CustomID: encodeAction(ActionUpdatePreview, customID.WithStickerYIncrement(panIncrementLarge)),
			})
		}
		if customID.Opts.Sticker.Y-panIncrementLarge >= 0 {
			stickerButtons = append(stickerButtons, discordgo.Button{
				Label: fmt.Sprintf("%dpx", panIncrementSmall),
				Emoji: &discordgo.ComponentEmoji{
					Name: "⬆",
				},
				Style:    discordgo.SecondaryButton,
				Disabled: false,
				CustomID: encodeAction(ActionUpdatePreview, customID.WithStickerYIncrement(0-panIncrementSmall)),
			})
		}
		if 336-(customID.Opts.Sticker.WidthOffset-widthIncrement) > 0 {
			stickerButtons = append(stickerButtons, discordgo.Button{
				Label: "Zoom",
				Emoji: &discordgo.ComponentEmoji{
					Name: "↔",
				},
				Style:    discordgo.SecondaryButton,
				Disabled: false,
				CustomID: encodeAction(ActionUpdatePreview, customID.WithStickerWidthIncrement(0-widthIncrement)),
			})
		}
	}
	captionButtons := []discordgo.MessageComponent{}
	if customID.Opts.Mode == customid.CaptionMode {
		captionButtons = append(captionButtons, discordgo.Button{
			Label:    "Set Caption",
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionOpenCaptionModal, customID),
		})
		captionButtons = append(captionButtons, discordgo.Button{
			Label:    "Toggle Subs",
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: mustEncodeUpdateStateAction(ToggleSubs, "true"),
		})
	}

	var modeSelectBtn discordgo.Button
	switch customID.Opts.Mode {
	case customid.NormalMode:
		modeSelectBtn = discordgo.Button{
			Label: "Next Mode (Sticker)",
			Emoji: &discordgo.ComponentEmoji{
				Name: "🖼",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithMode(customid.StickerMode)),
		}
	case customid.StickerMode:
		modeSelectBtn = discordgo.Button{
			Label: "Next Mode (Caption)",
			Emoji: &discordgo.ComponentEmoji{
				Name: "🖼",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithMode(customid.CaptionMode)),
		}
	case customid.CaptionMode:
		modeSelectBtn = discordgo.Button{
			Label: "Next Mode (Video)",
			Emoji: &discordgo.ComponentEmoji{
				Name: "🎦",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithMode(customid.VideoMode)),
		}
	default:
		modeSelectBtn = discordgo.Button{
			Label: "Next Mode (Normal)",
			Emoji: &discordgo.ComponentEmoji{
				Name: "🖼",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionUpdatePreview, customID.WithMode(customid.NormalMode)),
		}
	}

	postActions := []discordgo.MessageComponent{discordgo.Button{
		Label: "Post",
		Emoji: &discordgo.ComponentEmoji{
			Name: "✅",
		},
		Style:    discordgo.PrimaryButton,
		Disabled: false,
		CustomID: encodeAction(ActionConfirmPostGif, customID),
	}}
	if customID.Opts.Mode == customid.NormalMode {
		postActions = append(postActions, discordgo.Button{
			Label: "Post with Custom Text",
			Emoji: &discordgo.ComponentEmoji{
				Name: "✅",
			},
			Style:    discordgo.PrimaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionOpenCustomTextModal, customID),
		})
	}

	postActions = append(postActions,
		modeSelectBtn,
		discordgo.Button{
			Label: "Prev Result",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏫",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionPrevResult, customID),
		},
		discordgo.Button{
			Label: "Next Result",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏬",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionNextResult, customID),
		},
	)

	actions := []discordgo.MessageComponent{}
	if len(navigateButtons) > 0 && customID.Opts.Mode == customid.NormalMode {
		actions = append(actions, discordgo.ActionsRow{Components: navigateButtons})
	}
	if len(shiftButtons) > 0 && customID.Opts.Mode != customid.CaptionMode {
		actions = append(actions, discordgo.ActionsRow{Components: shiftButtons})
	}
	if len(extendButtons) > 0 && customID.Opts.Mode != customid.CaptionMode {
		actions = append(actions, discordgo.ActionsRow{Components: extendButtons})
	}
	if len(trimButtons) > 0 && customID.Opts.Mode != customid.CaptionMode {
		actions = append(actions, discordgo.ActionsRow{Components: trimButtons})
	}
	if len(stickerButtons) > 0 {
		actions = append(actions, discordgo.ActionsRow{Components: stickerButtons})
	}
	if len(captionButtons) > 0 {
		actions = append(actions, discordgo.ActionsRow{Components: captionButtons})
	}
	actions = append(actions, discordgo.ActionsRow{Components: postActions})

	return actions, nil
}

func (b *Bot) postCustomGif(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	var customText []string
	for k := range i.Interaction.ModalSubmitData().Components {
		customText = append(customText, i.Interaction.ModalSubmitData().Components[k].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value)
	}
	b.postGifWithOptions(s, i, rawCustomID, customText, "", false)
}

func (b *Bot) postWebm(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	var customText []string
	for k := range i.Interaction.ModalSubmitData().Components {
		customText = append(customText, i.Interaction.ModalSubmitData().Components[k].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value)
	}
	b.postGifWithOptions(s, i, rawCustomID, customText, "", true)
}

func (b *Bot) updateState(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	update, err := decodeUpdateStateAction(rawCustomID)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to decode state update: %w", err))
		return
	}
	b.updatePreviewWithOptions(s, i, update)
}

func (b *Bot) setCaption(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	b.updatePreviewWithOptions(
		s,
		i,
		&StateUpdate{
			Type:  SetCaption,
			Value: i.Interaction.ModalSubmitData().Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value,
		},
	)
}

func (b *Bot) postGifFromPreview(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	if rawCustomID == "" {
		b.respondError(s, i, fmt.Errorf("missing customID"))
		return
	}
	customID, err := customid.ParsePayload(rawCustomID)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid customID"))
		return
	}
	dialog, err := b.srtStore.GetDialogRange(customID.Publication, customID.Series, customID.Episode, customID.StartPosition, customID.EndPosition)
	if err != nil {
		b.respondError(
			s,
			i,
			fmt.Errorf("failed to fetch selected lines: %s", customID.String()),
			slog.String("err", err.Error()),
			slog.String("custom_id", rawCustomID),
		)
		return
	}
	var files []*discordgo.File
	if len(i.Message.Attachments) > 0 {
		attachment := i.Message.Attachments[0]
		image, err := http.Get(attachment.URL)
		if err != nil {
			b.respondError(s, i, fmt.Errorf("failed to get original message attachment: %w", err))
			return
		}
		defer image.Body.Close()

		files = append(files, &discordgo.File{
			Name:        attachment.Filename,
			Reader:      image.Body,
			ContentType: attachment.ContentType,
		})
	}
	interactionResponse := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: b.gifDescription(
				customID,
				uniqueUser(i.Member, i.User),
				dialog,
				false,
			),
			Files:       files,
			Attachments: util.ToPtr([]*discordgo.MessageAttachment{}),
		},
	}

	if err := s.InteractionRespond(i.Interaction, interactionResponse); err != nil {
		b.respondError(s, i, err)
		return
	}
}

func (b *Bot) postGifWithOptions(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string, customText []string, caption string, video bool) {
	if rawCustomID == "" {
		b.respondError(s, i, fmt.Errorf("missing customID"))
		return
	}
	customID, err := customid.ParsePayload(rawCustomID)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid customID"))
		return
	}
	dialog, err := b.srtStore.GetDialogRange(customID.Publication, customID.Series, customID.Episode, customID.StartPosition, customID.EndPosition)
	if err != nil {
		b.respondError(
			s,
			i,
			fmt.Errorf("failed to fetch selected lines: %s", customID.String()),
			slog.String("err", err.Error()),
			slog.String("custom_id", rawCustomID),
		)
		return
	}

	if err := b.completeResponse(s, i, dialog, uniqueUser(i.Member, i.User), customText, customID, video, caption); err != nil {
		b.logger.Error("Failed to complete media response", slog.String("err", err.Error()), slog.Bool("video", video))
	}
}

func (b *Bot) completeResponse(
	s *discordgo.Session,
	i *discordgo.InteractionCreate,
	dialog []model2.Dialog,
	username string,
	customText []string,
	customID *customid.Payload,
	video bool,
	caption string,
) error {
	interactionResponse, err := b.buildInteractionResponse(
		dialog,
		customID,
		nil,
		withUsername(username),
		withPlaceholder(),
		withVideo(video),
	)
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
		interactionResponse, err = b.buildInteractionResponse(
			dialog,
			customID,
			nil,
			withUsername(username),
			withCustomText(customText),
			withVideo(video),
			withCaption(caption))
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
	customID *customid.Payload,
	state *OutputState,
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
		gif, err := b.renderFile(state, dialog, opts.customText, customID, opts.outputFileType)
		if err != nil {
			return nil, err
		}
		files = []*discordgo.File{gif}
		bodyText = ""
	} else {
		bodyText = ":timer: Rendering..."
	}

	var info string
	if state.CustomID.Opts.Mode == customid.VideoMode {
		info = "\nNote: Most videos do not currently have audio."
	}

	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf(
				"%s\n\n%s\n%s%s",
				bodyText,
				b.gifDescription(customID, opts.username, dialog, opts.customText != nil),
				mustEncodeState(state),
				info,
			),
			Files:       files,
			Attachments: util.ToPtr([]*discordgo.MessageAttachment{}),
		},
	}, nil
}

func (b *Bot) gifDescription(customID *customid.Payload, username string, dialog []model2.Dialog, edited bool) string {
	editLabel := ""
	if edited {
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
	modeLabel := ""
	if customID.Opts.Mode != customid.NormalMode {
		modeLabel = fmt.Sprintf("(%s)", customID.Opts.Mode)
	}
	return fmt.Sprintf(
		"`%s@%s-%s%s%s%s%s` posted by `%s`",
		customID.DialogID(),
		dialog[0].StartTimestamp,
		dialog[len(dialog)-1].EndTimestamp,
		shiftLabel,
		extendLabel,
		editLabel,
		modeLabel,
		username,
	)
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

func (b *Bot) renderFile(state *OutputState, dialog []model2.Dialog, customText []string, customID *customid.Payload, outputFileType customid.OutputFileType) (*discordgo.File, error) {
	disableCaching := customID.Opts.ExtendOrTrim != 0 || customID.Opts.Shift != 0 || customText != nil || customID.Opts.Mode != customid.NormalMode

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
	logger.Debug("Rendering file...")

	options := []render.Option{
		render.WithCaching(disableCaching),
		render.WithCustomText(customText),
		render.WithStartTimestamp(startTimestamp),
		render.WithEndTimestamp(endTimestamp),
	}
	if customID.Opts.Mode == customid.CaptionMode {
		options = append(options,
			render.WithCaption(state.Caption),
			render.WithDisableSubs(state.DisableSubtitles),
		)
	}
	if customID.Opts.Mode == customid.VideoMode {
		options = append(options, render.WithOutputFileType(customid.OutputWebm))
	}

	file, err := b.renderer.RenderFile(
		dialog[0].VideoFileName,
		customID,
		dialog,
		options...,
	)
	if err != nil {
		b.logger.Error("failed to render file", slog.String("err", err.Error()))
		return nil, err
	}
	return file, nil
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

func (b *Bot) nextResult(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	b.nextOrPreviousResult(s, i, rawCustomID, true)
}

func (b *Bot) previousResult(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string) {
	b.nextOrPreviousResult(s, i, rawCustomID, false)
}

func (b *Bot) nextOrPreviousResult(s *discordgo.Session, i *discordgo.InteractionCreate, rawCustomID string, next bool) {

	if i.Type != discordgo.InteractionMessageComponent {
		return
	}
	if rawCustomID == "" {
		b.respondError(s, i, fmt.Errorf("missing customID"))
		return
	}
	customID, err := customid.ParsePayload(rawCustomID)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid customID"))
		return
	}
	state, err := extractStateFromBody(i.Message.Content)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to get current state"))
		return
	}

	terms, err := searchterms.Parse(state.OriginalTerms)
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
	if state.OriginalPosition != nil {
		for k, v := range res {
			if v.ID == customID.DialogIDWithRange(*state.OriginalPosition) {
				currentSelection = k
			}
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
		b.updatePreviewWithOptions(
			s,
			i,
			&StateUpdate{
				Type:  UpdateCustomID,
				Value: rawCustomID,
			},
		)
		return
	}

	b.updatePreviewWithOptions(
		s,
		i,
		&StateUpdate{
			Type:  ResetCustomID,
			Value: res[nextSelection].ID,
		},
	)
}

func encodeAction(action Action, customID *customid.Payload) string {
	return fmt.Sprintf("%s:%s", action, customID.String())
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

func mustEncodeState(s *OutputState) string {
	if s == nil {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("||%s||", string(b))
}

func decodeState(raw string) (*OutputState, error) {
	state := &OutputState{}
	err := json.Unmarshal([]byte(strings.Trim(raw, "|")), state)
	if err != nil {
		return nil, err
	}
	return state, nil
}

func extractStateFromBody(msgContent string) (*OutputState, error) {
	foundState := extractState.FindString(msgContent)
	if foundState == "" {
		return nil, fmt.Errorf("failed to find state in message body")
	}

	state, err := decodeState(foundState)
	if err != nil {
		return nil, fmt.Errorf("failed to parse state: %s", foundState)
	}

	return state, nil
}

type StateUpdateType string

const SetSearchResPosFromCID = StateUpdateType("set_search_res_pos_from_cid")
const ToggleSubs = StateUpdateType("toggle_subs")
const SetCaption = StateUpdateType("set_caption")
const UpdateCustomID = StateUpdateType("update_custom_id")
const ResetCustomID = StateUpdateType("reset_custom_id")

type StateUpdate struct {
	Type  StateUpdateType
	Value string
}

func mustEncodeUpdateStateAction(tpe StateUpdateType, value string) string {
	enc, err := json.Marshal(StateUpdate{Type: tpe, Value: value})
	if err != nil {
		panic(fmt.Sprintf("failed to encode state update: %s", err.Error()))
	}
	return fmt.Sprintf("%s:%s", ActionUpdateState, string(enc))
}

func decodeUpdateStateAction(encoded string) (*StateUpdate, error) {
	upd := &StateUpdate{}
	return upd, json.Unmarshal([]byte(strings.TrimPrefix(encoded, fmt.Sprintf("%s:", ActionUpdateState))), upd)
}
