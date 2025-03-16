package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bwmarrin/discordgo"
	"github.com/warmans/tvgif/pkg/discord/media"
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
	"strconv"
	"strings"
	"sync"
	"time"
)

const SubSeparator = "---"

type Command string

const (
	CommandSearch Command = "tvgif"
	CommandHelp   Command = "tvgif-help"
	CommandDelete Command = "tvgif-delete"
)

type Action string

const (
	ActionConfirmPost = Action("cfrmg")
	ActionNextResult  = Action("nxt")
	ActionPrevResult  = Action("prv")
	ActionUpdateState = Action("sta")
)

const (
	ActionOpenCustomTextModal = Action("cstm")
	ActionOpenCaptionModal    = Action("ctm")
	ActionOpenExtendTrimModal = Action("oem")
)

const (
	ModalSetSubs              = Action("m_ss")
	ModalActionSetExtendValue = Action("m_sev")
	ModalSetCaption           = Action("m_sc")
)

var postedByUser = regexp.MustCompile(`.+ posted by \x60([^\x60]+)\x60`)
var extractState = regexp.MustCompile(`\|\|(\{.*\})\|\|`)

var rendersInProgress = map[string]string{}
var renderMutex = sync.RWMutex{}
var errRenderInProgress = errors.New("render in progress")
var errDuplicateInteraction = errors.New("interaction already processing")

func resolveResponseOptions(opts ...responseOption) *responseOptions {
	options := &responseOptions{
		username: "unknown",
	}
	for _, o := range opts {
		o(options)
	}
	return options
}

type responseOptions struct {
	username    string
	placeholder bool
	preview     bool
}

type responseOption func(options *responseOptions)

func responseWithUsername(username string) responseOption {
	return func(options *responseOptions) {
		options.username = username
	}
}

func responseWithPlaceholder() responseOption {
	return func(options *responseOptions) {
		options.placeholder = true
	}
}

func responseWithPreview() responseOption {
	return func(options *responseOptions) {
		options.preview = true
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
	bot.buttonHandlers = map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, payload string){
		ActionConfirmPost:         bot.btnPostFromPreview,
		ActionNextResult:          bot.btnNextResult,
		ActionPrevResult:          bot.btnPreviewResult,
		ActionOpenCustomTextModal: bot.btnOpenCustomTextModal,
		ActionOpenCaptionModal:    bot.btnOpenCaptionModal,
		ActionOpenExtendTrimModal: bot.btnOpenExtendModal,
		ActionUpdateState:         bot.btnUpdateState,
	}
	bot.modalHandlers = map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		ModalSetSubs:              bot.handleModalSetSubs,
		ModalSetCaption:           bot.handleModalSetCaption,
		ModalActionSetExtendValue: bot.handleModalSetExtendTrimValue,
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
	buttonHandlers  map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate, payload string)
	modalHandlers   map[Action]func(s *discordgo.Session, i *discordgo.InteractionCreate)
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
					h(s, i)
					return
				}
			}
			b.respondError(s, i, fmt.Errorf("unknown modal submission: %s", i.ModalSubmitData().CustomID))
			return
		case discordgo.InteractionMessageComponent:
			// prefix match buttons to allow additional data in the mediaID
			for k, h := range b.buttonHandlers {
				actionPrefix := fmt.Sprintf("%s:", k)
				if strings.HasPrefix(i.MessageComponentData().CustomID, actionPrefix) {
					b.logger.Debug("handle button", slog.String("payload", i.MessageComponentData().CustomID))
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
			b.respondError(s, i, fmt.Errorf("no dialog was selected"))
			return
		}

		result := &struct {
			Terms string
			ID    string
		}{}
		err := json.Unmarshal([]byte(selection), result)
		if err != nil {
			b.respondError(
				s,
				i,
				errors.New("failed to parse selection. Select an item from the dropdown"),
				slog.String("err", err.Error()),
			)
			return
		}

		username := uniqueUser(i.Member, i.User)
		mediaID, err := media.ParseID(result.ID)
		if err != nil {
			b.respondError(s, i, fmt.Errorf("invalid selection: %s", result.ID))
			return
		}

		b.logger.Info("Creating...", slog.String("custom_id", mediaID.String()))
		if err := b.createPreview(s, i, username, mediaID, result.Terms); err != nil {
			b.respondError(
				s,
				i,
				fmt.Errorf("failed to begin response"),
				slog.String("err", err.Error()),
			)
			return
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

func (b *Bot) updatePreview(s *discordgo.Session, i *discordgo.InteractionCreate, upds ...StateUpdate) {
	username := uniqueUser(i.Member, i.User)

	sta, err := extractStateFromBody(i.Message.Content)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to get current state"))
		return
	}

	for _, upd := range upds {
		if err := sta.ApplyUpdate(upd); err != nil {
			b.respondError(s, i, err)
			return
		}
	}

	dialogWithContext, err := b.getDialogWithContext(sta.ID)
	if err != nil {
		b.respondError(
			s, i,
			err,
			slog.String("err", err.Error()),
			slog.String("media_id", sta.ID.String()),
		)
		return
	}

	interactionResponse, err := b.buildInteractionResponse(
		dialogWithContext,
		sta,
		responseWithUsername(username),
		responseWithPlaceholder(),
		responseWithPreview(),
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
			dialogWithContext,
			sta,
			responseWithUsername(username),
			responseWithPreview(),
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
		buttons, err := b.createButtons(dialogWithContext.Dialog, sta)
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

func (b *Bot) createPreview(
	s *discordgo.Session,
	i *discordgo.InteractionCreate,
	username string,
	mediaID *media.ID,
	originalTerms string,
) error {
	state := &PreviewState{
		ID:               mediaID,
		OriginalTerms:    originalTerms,
		OriginalPosition: util.ToPtr(mediaID.PositionRange()),
		Settings: Settings{
			// defaults
			OutputFormat: OutputWebp,
		},
	}

	dialogWithContext, err := b.getDialogWithContext(state.ID)
	if err != nil {
		b.respondError(s, i, err)
		return err
	}

	// send a placeholder
	interactionResponse, err := b.buildInteractionResponse(
		dialogWithContext,
		state,
		responseWithUsername(username),
		responseWithPlaceholder(),
		responseWithPreview(),
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
			dialogWithContext,
			state,
			responseWithUsername(username),
			responseWithPreview(),
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

		buttons, err := b.createButtons(dialogWithContext.Dialog, state)
		if err != nil {
			b.logger.Error("edit failed. Failed to create buttons", slog.String("err", err.Error()))
			return
		}
		_, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content:    util.ToPtr(interactionResponse.Data.Content),
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

func (b *Bot) btnOpenCustomTextModal(s *discordgo.Session, i *discordgo.InteractionCreate, rawMediaID string) {
	mediaID, err := media.ParseID(rawMediaID)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid mediaID"))
		return
	}
	dialog, err := b.srtStore.GetDialogRange(
		mediaID.Publication,
		mediaID.Series,
		mediaID.Episode,
		mediaID.StartPosition,
		mediaID.EndPosition,
	)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to fetch original dialog"))
		return
	}
	state, err := extractStateFromBody(i.Message.Content)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to get current state"))
		return
	}

	content := []string{}
	for k, d := range dialog {
		content = append(content, d.Content)
		// override a sub with an existing custom sub
		if state.Settings.OverrideSubs != nil && len(state.Settings.OverrideSubs) > k {
			content[k] = state.Settings.OverrideSubs[k]
		}
	}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: encodeAction(ModalSetSubs, mediaID),
			Title:    "Edit Subs",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID: "custom_text",
							Label:    "Subtitles",
							Style:    discordgo.TextInputParagraph,
							Required: false,
							Value:    strings.Join(content, "\n---\n"),
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

func (b *Bot) btnOpenCaptionModal(s *discordgo.Session, i *discordgo.InteractionCreate, rawMediaID string) {
	mediaID, err := media.ParseID(rawMediaID)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid mediaID"))
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
				Value:       state.Settings.Caption,
				Placeholder: "Caption added to top of image",
			},
		},
	}}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID:   encodeAction(ModalSetCaption, mediaID),
			Title:      "Set Caption",
			Components: fields,
		},
	})
	if err != nil {
		b.respondError(s, i, err)
	}
}

func (b *Bot) btnOpenExtendModal(s *discordgo.Session, i *discordgo.InteractionCreate, rawMediaID string) {
	state, err := extractStateFromBody(i.Message.Content)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to get current state"))
		return
	}

	b.openGenericValueModal(
		s,
		i,
		rawMediaID,
		ModalActionSetExtendValue,
		"Extend/Trim (Seconds e.g. 1.0/-1.0)",
		fmt.Sprintf("%0.2f", float64(state.Settings.ExtendOrTrim)/float64(time.Second)),
	)
}

func (b *Bot) openGenericValueModal(
	s *discordgo.Session,
	i *discordgo.InteractionCreate,
	rawMediaID string,
	action Action,
	label string,
	initialValue string,
) {
	mediaID, err := media.ParseID(rawMediaID)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid mediaID"))
		return
	}
	fields := []discordgo.MessageComponent{discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.TextInput{
				CustomID:  "value",
				Label:     label,
				Style:     discordgo.TextInputShort,
				Required:  true,
				MaxLength: 128,
				Value:     initialValue,
			},
		},
	}}
	err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID:   encodeAction(action, mediaID),
			Title:      "Set Value",
			Components: fields,
		},
	})
	if err != nil {
		b.respondError(s, i, err)
	}
}

func (b *Bot) createButtons(dialog []model2.Dialog, state *PreviewState) ([]discordgo.MessageComponent, error) {

	before, after, err := b.srtStore.GetDialogContext(
		state.ID.Publication,
		state.ID.Series,
		state.ID.Episode,
		state.ID.StartPosition,
		state.ID.EndPosition,
		1,
		3,
	)
	if err != nil {
		return nil, err
	}

	dialogDuration := (dialog[len(dialog)-1].EndTimestamp - dialog[0].StartTimestamp) + state.Settings.ExtendOrTrim

	navigateButtons := []discordgo.MessageComponent{}
	if len(before) > 0 {
		prevCustomID, err := media.ParseID(before[0].ID(state.ID.EpisodeID()))
		if err != nil {
			return nil, err
		}
		navigateButtons = append(navigateButtons, discordgo.Button{
			Label: "Previous Sub",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏪",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: StateSetMediaID(prevCustomID).CustomID(),
		})
	}
	if len(after) > 0 {
		nextMediaID, err := media.ParseID(after[0].ID(state.ID.EpisodeID()))
		if err != nil {
			return nil, err
		}
		navigateButtons = append(navigateButtons, discordgo.Button{
			Label: "Next Sub",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: StateSetMediaID(state.ID.WithStartPosition(state.ID.StartPosition + 1)).CustomID(),
		})
		if dialogDuration+(after[0].EndTimestamp-after[0].StartTimestamp) <= limits.MaxGifDuration {
			navigateButtons = append(navigateButtons, discordgo.Button{
				Label: "Merge Next Sub",
				Emoji: &discordgo.ComponentEmoji{
					Name: "➕",
				},
				Style:    discordgo.SecondaryButton,
				Disabled: false,
				CustomID: StateSetMediaID(state.ID.WithEndPosition(nextMediaID.StartPosition)).CustomID(),
			})
		}
		if dialogDuration+(after[len(after)-1].EndTimestamp-after[len(after)-1].StartTimestamp) <= limits.MaxGifDuration {
			navigateButtons = append(navigateButtons, discordgo.Button{
				Label: fmt.Sprintf("Merge Next %d Subs", len(after)),
				Emoji: &discordgo.ComponentEmoji{
					Name: "➕",
				},
				Style:    discordgo.SecondaryButton,
				Disabled: false,
				CustomID: StateSetMediaID(state.ID.WithEndPosition(nextMediaID.StartPosition + int64(len(after)))).CustomID(),
			})
		}
		if state.ID.EndPosition > state.ID.StartPosition {
			navigateButtons = append(navigateButtons, discordgo.Button{
				Label: "Last Sub",
				Emoji: &discordgo.ComponentEmoji{
					Name: "✂",
				},
				Style:    discordgo.SecondaryButton,
				Disabled: false,
				CustomID: StateSetMediaID(state.ID.WithEndPosition(state.ID.EndPosition - 1)).CustomID(),
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
			CustomID: StateSetShift(state.Settings.Shift + (0 - (time.Second * 5))).CustomID(),
		},
		discordgo.Button{
			Label: "1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏪",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: StateSetShift(state.Settings.Shift + (0 - time.Second)).CustomID(),
		},
		discordgo.Button{
			Label: "0.5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: StateSetShift(state.Settings.Shift + (time.Second / 2)).CustomID(),
		},
		discordgo.Button{
			Label: "1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: StateSetShift(state.Settings.Shift + time.Second).CustomID(),
		},
		discordgo.Button{
			Label: "5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏩",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: StateSetShift(state.Settings.Shift + time.Second*5).CustomID(),
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
			CustomID: StateSetExtendOrTrim(state.Settings.ExtendOrTrim + (time.Second / 2)).CustomID(),
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
			CustomID: StateSetExtendOrTrim(state.Settings.ExtendOrTrim + time.Second).CustomID(),
		})
	}
	if dialogDuration-(time.Second/2) > 0 {
		extendButtons = append(extendButtons, discordgo.Button{
			Label: "-0.5s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "✂",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: StateSetExtendOrTrim(state.Settings.ExtendOrTrim - (time.Second / 2)).CustomID(),
		})
	}
	if dialogDuration-time.Second > 0 {
		extendButtons = append(extendButtons, discordgo.Button{
			Label: "-1s",
			Emoji: &discordgo.ComponentEmoji{
				Name: "✂",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: StateSetExtendOrTrim(state.Settings.ExtendOrTrim - time.Second).CustomID(),
		})
	}
	extendButtons = append(extendButtons, discordgo.Button{
		Label: "Custom",
		Emoji: &discordgo.ComponentEmoji{
			Name: "➕",
		},
		Style:    discordgo.SecondaryButton,
		Disabled: false,
		CustomID: encodeAction(ActionOpenExtendTrimModal, state.ID),
	})
	formatButtons := []discordgo.MessageComponent{
		discordgo.Button{
			Label: "WebP",
			Emoji: &discordgo.ComponentEmoji{
				Name: "🖼️",
			},
			Style:    successBtnIfTrue(state.Settings.OutputFormat == OutputWebp || state.Settings.OutputFormat == OutputDefault),
			Disabled: false,
			CustomID: StateSetOutputFormat(OutputWebp).CustomID(),
		},
		discordgo.Button{
			Label: "Gif",
			Emoji: &discordgo.ComponentEmoji{
				Name: "🖼️",
			},
			Style:    successBtnIfTrue(state.Settings.OutputFormat == OutputGif),
			Disabled: false,
			CustomID: StateSetOutputFormat(OutputGif).CustomID(),
		},
		//discordgo.Button{
		//	Label: "Sticker",
		//	Emoji: &discordgo.ComponentEmoji{
		//		Name: "🎨",
		//	},
		//	Style:    successBtnIfTrue(state.Settings.Mode == StickerMode),
		//	Disabled: false,
		//	CustomID: StateSetMode(StickerMode).CustomID(),
		//},
		discordgo.Button{
			Label: "Normal",
			Emoji: &discordgo.ComponentEmoji{
				Name: "🎨",
			},
			Style:    successBtnIfTrue(state.Settings.Mode == NormalMode),
			Disabled: false,
			CustomID: StateSetMode(NormalMode).CustomID(),
		},
		discordgo.Button{
			Label: "Caption",
			Emoji: &discordgo.ComponentEmoji{
				Name: "🎨",
			},
			Style:    successBtnIfTrue(state.Settings.Mode == CaptionMode),
			Disabled: false,
			CustomID: StateSetMode(CaptionMode).CustomID(),
		},
	}

	captionButtons := []discordgo.MessageComponent{}
	if state.Settings.Mode == CaptionMode {
		captionButtons = append(captionButtons, discordgo.Button{
			Label:    "Set Caption",
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionOpenCaptionModal, state.ID),
		})
		captionButtons = append(captionButtons, discordgo.Button{
			Label:    "Toggle Subs",
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: StateSetSubsEnabled(!state.Settings.SubsEnabled).CustomID(),
		})
	}

	postActions := []discordgo.MessageComponent{discordgo.Button{
		Label: "Post",
		Emoji: &discordgo.ComponentEmoji{
			Name: "✅",
		},
		Style:    discordgo.PrimaryButton,
		Disabled: false,
		CustomID: encodeAction(ActionConfirmPost, state.ID),
	}}
	if state.Settings.Mode != StickerMode {
		postActions = append(postActions, discordgo.Button{
			Label: "Edit Subs",
			Emoji: &discordgo.ComponentEmoji{
				Name: "📝",
			},
			Style:    successBtnIfTrue(state.Settings.OverrideSubs != nil),
			Disabled: false,
			CustomID: encodeAction(ActionOpenCustomTextModal, state.ID),
		})
	}

	postActions = append(postActions,
		discordgo.Button{
			Label: "Prev Result",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏫",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionPrevResult, state.ID),
		},
		discordgo.Button{
			Label: "Next Result",
			Emoji: &discordgo.ComponentEmoji{
				Name: "⏬",
			},
			Style:    discordgo.SecondaryButton,
			Disabled: false,
			CustomID: encodeAction(ActionNextResult, state.ID),
		},
	)

	actions := []discordgo.MessageComponent{}
	if len(navigateButtons) > 0 && state.Settings.Mode == NormalMode {
		actions = append(actions, discordgo.ActionsRow{Components: navigateButtons})
	}
	if len(shiftButtons) > 0 && state.Settings.Mode != CaptionMode {
		actions = append(actions, discordgo.ActionsRow{Components: shiftButtons})
	}
	if len(extendButtons) > 0 && state.Settings.Mode != CaptionMode {
		actions = append(actions, discordgo.ActionsRow{Components: extendButtons})
	}

	stickerButtons := b.stickerButtons(state)
	if len(stickerButtons) > 0 {
		actions = append(actions, discordgo.ActionsRow{Components: stickerButtons})
	}
	if len(captionButtons) > 0 {
		actions = append(actions, discordgo.ActionsRow{Components: captionButtons})
	}
	if len(formatButtons) > 0 {
		actions = append(actions, discordgo.ActionsRow{Components: formatButtons})
	}
	actions = append(actions, discordgo.ActionsRow{Components: postActions})

	return actions, nil
}

func (b *Bot) stickerButtons(state *PreviewState) []discordgo.MessageComponent {
	//const panIncrementLarge = 50
	//const panIncrementSmall = 25
	//const widthIncrement = 50
	stickerButtons := []discordgo.MessageComponent{}
	//if state.Settings.Mode == StickerMode {
	//	if state.Settings.Sticker.X+panIncrementLarge <= 596 {
	//		stickerButtons = append(stickerButtons, discordgo.Button{
	//			Label: fmt.Sprintf("%dpx", panIncrementLarge),
	//			Emoji: &discordgo.ComponentEmoji{
	//				Name: "➡",
	//			},
	//			Style:    discordgo.SecondaryButton,
	//			Disabled: false,
	//			CustomID: encodeAction(ActionUpdateMediaID, state.ID.WithStickerXIncrement(panIncrementLarge)),
	//		})
	//	}
	//	if state.Settings.Sticker.X-panIncrementLarge >= 0 {
	//		stickerButtons = append(stickerButtons, discordgo.Button{
	//			Label: fmt.Sprintf("%dpx", panIncrementSmall),
	//			Emoji: &discordgo.ComponentEmoji{
	//				Name: "⬅",
	//			},
	//			Style:    discordgo.SecondaryButton,
	//			Disabled: false,
	//			CustomID: encodeAction(ActionUpdateMediaID, customID.WithStickerXIncrement(0-panIncrementSmall)),
	//		})
	//	}
	//	if customID.Opts.Sticker.Y+panIncrementLarge <= 336 {
	//		stickerButtons = append(stickerButtons, discordgo.Button{
	//			Label: fmt.Sprintf("%dpx", panIncrementLarge),
	//			Emoji: &discordgo.ComponentEmoji{
	//				Name: "⬇",
	//			},
	//			Style:    discordgo.SecondaryButton,
	//			Disabled: false,
	//			CustomID: encodeAction(ActionUpdateMediaID, customID.WithStickerYIncrement(panIncrementLarge)),
	//		})
	//	}
	//	if customID.Opts.Sticker.Y-panIncrementLarge >= 0 {
	//		stickerButtons = append(stickerButtons, discordgo.Button{
	//			Label: fmt.Sprintf("%dpx", panIncrementSmall),
	//			Emoji: &discordgo.ComponentEmoji{
	//				Name: "⬆",
	//			},
	//			Style:    discordgo.SecondaryButton,
	//			Disabled: false,
	//			CustomID: encodeAction(ActionUpdateMediaID, customID.WithStickerYIncrement(0-panIncrementSmall)),
	//		})
	//	}
	//	if 336-(customID.Opts.Sticker.WidthOffset-widthIncrement) > 0 {
	//		stickerButtons = append(stickerButtons, discordgo.Button{
	//			Label: "Zoom",
	//			Emoji: &discordgo.ComponentEmoji{
	//				Name: "↔",
	//			},
	//			Style:    discordgo.SecondaryButton,
	//			Disabled: false,
	//			CustomID: encodeAction(ActionUpdateMediaID, customID.WithStickerWidthIncrement(0-widthIncrement)),
	//		})
	//	}
	//}

	return stickerButtons
}

func (b *Bot) btnUpdateState(s *discordgo.Session, i *discordgo.InteractionCreate, payload string) {
	update, err := decodeUpdateStateAction(payload)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to decode state update: %w", err))
		return
	}
	b.updatePreview(s, i, update)
}

func (b *Bot) handleModalSetCaption(s *discordgo.Session, i *discordgo.InteractionCreate) {
	b.updatePreview(
		s,
		i,
		StateSetCaption(
			i.Interaction.ModalSubmitData().Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value,
		),
	)
}

func (b *Bot) handleModalSetSubs(s *discordgo.Session, i *discordgo.InteractionCreate) {
	b.updatePreview(
		s,
		i,
		StateSetSubs(
			strings.Split(
				i.Interaction.ModalSubmitData().Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value,
				SubSeparator,
			),
		),
	)
}

func (b *Bot) handleModalSetExtendTrimValue(s *discordgo.Session, i *discordgo.InteractionCreate) {
	strVal := i.Interaction.ModalSubmitData().Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value
	floatVal, err := strconv.ParseFloat(strVal, 64)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid extend/trim value %s: %w", strVal, err))
		return
	}
	if floatVal > float64(limits.MaxGifDuration) {
		b.respondError(s, i, fmt.Errorf("invalid extend/trim value %s: cannot exceed max gif duration", strVal, err))
		return
	}

	b.updatePreview(s, i, StateSetExtendOrTrim(time.Duration(floatVal*float64(time.Second))))
}

func (b *Bot) btnPostFromPreview(s *discordgo.Session, i *discordgo.InteractionCreate, payload string) {
	state, err := extractStateFromBody(i.Message.Content)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("failed to get preview state"))
		return
	}
	dialogWithContext, err := b.getDialogWithContext(state.ID)
	if err != nil {
		b.respondError(
			s,
			i,
			fmt.Errorf("failed to fetch selected lines: %s", state.ID.String()),
			slog.String("err", err.Error()),
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
			Content: b.mediaDescription(
				state,
				uniqueUser(i.Member, i.User),
				dialogWithContext,
				state.Settings.OverrideSubs != nil,
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

func (b *Bot) buildInteractionResponse(
	dialogWithContext *DialogWithContext,
	state *PreviewState,
	options ...responseOption,
) (*discordgo.InteractionResponse, error) {

	opts := resolveResponseOptions(options...)

	cleanup, err := lockRenderer(opts.username, state.ID.String())
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
		gif, err := b.renderFile(state, dialogWithContext.Dialog)
		if err != nil {
			return nil, err
		}
		files = []*discordgo.File{gif}
		bodyText = ""
	} else {
		bodyText = ":timer: Rendering..."
	}

	var info string
	if state.ID != nil && state.Settings.Mode == VideoMode {
		info = "\nNote: Most videos do not currently have audio."
	}

	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf(
				"%s\n\n%s\n%s%s",
				bodyText,
				b.mediaDescription(
					state,
					opts.username,
					dialogWithContext,
					state.Settings.OverrideSubs != nil,
					opts.preview,
				),
				mustEncodeState(state),
				info,
			),
			Files:       files,
			Attachments: util.ToPtr([]*discordgo.MessageAttachment{}),
		},
	}, nil
}

func (b *Bot) mediaDescription(state *PreviewState, username string, dialogWithContext *DialogWithContext, edited bool, preview bool) string {
	editLabel := ""
	if edited {
		editLabel = " (edited)"
	}
	extendLabel := ""
	if state.Settings.ExtendOrTrim != 0 {
		if state.Settings.ExtendOrTrim > 0 {
			extendLabel = fmt.Sprintf("(+%s)", state.Settings.ExtendOrTrim.String())
		} else {
			extendLabel = fmt.Sprintf("(%s)", state.Settings.ExtendOrTrim.String())
		}
	}
	shiftLabel := ""
	if state.Settings.Shift != 0 {
		if state.Settings.Shift > 0 {
			shiftLabel = fmt.Sprintf("(>>%s)", state.Settings.Shift.String())
		} else {
			shiftLabel = fmt.Sprintf("(<<%s)", state.Settings.Shift.String())
		}
	}
	modeLabel := ""
	if state.Settings.Mode != NormalMode {
		modeLabel = fmt.Sprintf("(%s)", state.Settings.Mode)
	}

	dialogText := ""
	if preview {
		dialogText = dialogWithContext.String()
	}

	return fmt.Sprintf(
		"`%s@%s-%s%s%s%s%s` posted by `%s`\n\n%s",
		state.ID.DialogID(),
		dialogWithContext.Dialog[0].StartTimestamp,
		dialogWithContext.Dialog[len(dialogWithContext.Dialog)-1].EndTimestamp,
		shiftLabel,
		extendLabel,
		editLabel,
		modeLabel,
		username,
		dialogText,
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

func (b *Bot) renderFile(state *PreviewState, dialog []model2.Dialog) (*discordgo.File, error) {

	disableCaching := state.Settings.ExtendOrTrim != 0 || state.Settings.Shift != 0 || state.Settings.OverrideSubs != nil || (state.Settings.Mode != NormalMode)
	startTimestamp := dialog[0].StartTimestamp
	endTimestamp := dialog[len(dialog)-1].EndTimestamp

	if state.Settings.Shift != 0 {
		startTimestamp += state.Settings.Shift
		endTimestamp += state.Settings.Shift
	}
	if state.Settings.ExtendOrTrim != 0 {
		endTimestamp += state.Settings.ExtendOrTrim
		if endTimestamp <= startTimestamp {
			endTimestamp = startTimestamp + time.Second
		}
	}
	if endTimestamp-startTimestamp > limits.MaxGifDuration {
		endTimestamp = startTimestamp + limits.MaxGifDuration
	}

	logger := b.logger.With(
		slog.String("cache_key", state.ID.DialogID()),
		slog.String("source", dialog[0].VideoFileName),
		slog.Duration("from", startTimestamp),
		slog.Duration("to", endTimestamp),
		slog.String("output", string(state.Settings.OutputFormat)),
		slog.Bool("custom_text", state.Settings.OverrideSubs != nil),
	)
	logger.Debug("Rendering file...")

	options := []render.Option{
		render.WithCaching(disableCaching),
		render.WithCustomText(state.Settings.OverrideSubs),
		render.WithStartTimestamp(startTimestamp),
		render.WithEndTimestamp(endTimestamp),
	}
	if state.Settings.Mode == CaptionMode {
		options = append(options,
			render.WithCaptionMode(true),
			render.WithCaption(state.Settings.Caption),
			render.WithDisableSubs(state.Settings.SubsEnabled),
		)
	}
	if state.Settings.Mode == StickerMode {
		var opts *render.StickerModeOpts

		if state.Settings.Sticker != nil {
			opts = &render.StickerModeOpts{
				X:           state.Settings.Sticker.X,
				Y:           state.Settings.Sticker.Y,
				WidthOffset: state.Settings.Sticker.WidthOffset,
			}
		}
		options = append(options,
			render.WithStickerMode(true, opts),
		)
	}
	switch state.Settings.OutputFormat {
	case OutputGif:
		options = append(options, render.WithOutputFileType(render.OutputGif))
	case OutputWebm:
		options = append(options, render.WithOutputFileType(render.OutputWebm))
	default:
		options = append(options, render.WithOutputFileType(render.OutputWebp))
	}

	file, err := b.renderer.RenderFile(
		dialog[0].VideoFileName,
		state.ID,
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

func (b *Bot) btnNextResult(s *discordgo.Session, i *discordgo.InteractionCreate, rawMediaID string) {
	b.nextOrPreviousResult(s, i, rawMediaID, true)
}

func (b *Bot) btnPreviewResult(s *discordgo.Session, i *discordgo.InteractionCreate, rawMediaID string) {
	b.nextOrPreviousResult(s, i, rawMediaID, false)
}

func (b *Bot) nextOrPreviousResult(s *discordgo.Session, i *discordgo.InteractionCreate, rawMediaID string, next bool) {

	if i.Type != discordgo.InteractionMessageComponent {
		return
	}
	if rawMediaID == "" {
		b.respondError(s, i, fmt.Errorf("missing mediaID"))
		return
	}
	mediaID, err := media.ParseID(rawMediaID)
	if err != nil {
		b.respondError(s, i, fmt.Errorf("invalid mediaID"))
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
			if v.ID == mediaID.DialogIDWithRange(*state.OriginalPosition) {
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
		b.updatePreview(s, i, StateSetMediaID(mediaID))
		return
	}

	b.updatePreview(s, i, StateResetMediaID(res[nextSelection].ID))
}

func (b *Bot) getDialogWithContext(mediaID *media.ID) (*DialogWithContext, error) {
	dialog, err := b.srtStore.GetDialogRange(
		mediaID.Publication,
		mediaID.Series,
		mediaID.Episode,
		mediaID.StartPosition,
		mediaID.EndPosition,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch selected lines: %s", mediaID.String())
	}
	if len(dialog) == 0 {
		return nil, fmt.Errorf("no dialog was selected")
	}

	before, after, err := b.srtStore.GetDialogContext(
		mediaID.Publication,
		mediaID.Series,
		mediaID.Episode,
		mediaID.StartPosition,
		mediaID.EndPosition,
		3,
		3,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get dialog context: %w", err)
	}

	return &DialogWithContext{
		Before: before,
		Dialog: dialog,
		After:  after,
	}, nil
}

func encodeAction(action Action, mediaID *media.ID) string {
	return fmt.Sprintf("%s:%s", action, mediaID.String())
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

func mustEncodeState(s *PreviewState) string {
	if s == nil {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("||%s||", string(b))
}

func decodeState(raw string) (*PreviewState, error) {
	state := &PreviewState{}
	err := json.Unmarshal([]byte(strings.Trim(raw, "|")), state)
	if err != nil {
		return nil, err
	}
	return state, nil
}

func extractStateFromBody(msgContent string) (*PreviewState, error) {
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

func successBtnIfTrue(condition bool) discordgo.ButtonStyle {
	if condition {
		return discordgo.SuccessButton
	}
	return discordgo.SecondaryButton
}

type DialogWithContext struct {
	Before []model2.Dialog
	Dialog []model2.Dialog
	After  []model2.Dialog
}

func (d *DialogWithContext) String() string {
	out := &strings.Builder{}
	for _, v := range d.Before {
		fmt.Fprintf(out, "> %s (%s)\n", util.CleanDialogLine(v.Content), (v.EndTimestamp - v.StartTimestamp).String())
	}
	for _, v := range d.Dialog {
		fmt.Fprintf(out, "> **%s (%s)**\n", util.CleanDialogLine(v.Content), (v.EndTimestamp - v.StartTimestamp).String())
	}
	for _, v := range d.After {
		fmt.Fprintf(out, "> %s (%s)\n", util.CleanDialogLine(v.Content), (v.EndTimestamp - v.StartTimestamp).String())
	}
	return out.String()
}
