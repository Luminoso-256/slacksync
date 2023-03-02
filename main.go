package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/webhook"

	"github.com/slack-go/slack"
)

type slackUser struct {
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	Id       string `json:"id"`
	Profile  struct {
		Image48 string `json:"image_48"`
	} `json:"profile"`
}
type slackChannel struct {
	Id   string `json:"id"`
	Name string `json:"name"`
}
type RTEList struct {
	Type     string `json:"type"`
	Elements []struct {
		Type     string `json:"type"`
		Elements []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"elements"`
	} `json:"elements"`
	Style  string `json:"style"`
	Indent int    `json:"indent"`
	Border int    `json:"border"`
}

type BotConfig struct {
	Revision       string            `json:"revision"`
	MirroredHooks  map[string]string `json:"mirrored_hooks"`
	StatusHook     string            `json:"status_hook"`
	ChannelConfigs map[string]struct {
		Id  string `json:"id"`
		Key string `json:"key"`
	} `json:"channel_confs"`
	SlackKeys map[string]string `json:"slack_keys"`
}

var (
	config          BotConfig
	slack_msgs      = make(map[string][]slack.Message)
	discord_clients = make(map[string]webhook.Client)
	slack_clients   = make(map[string]*slack.Client)

	users    map[string]slackUser
	channels map[string]slackChannel
	groups   map[string]string

	slack_escapes = map[string]string{
		"&amp;": "&",
		"&gt;":  ">",
		"&lt;":  "<",
		"%7c":   "|",
	}
)

//https://github.com/slack-go/slack/pull/55/commits/17d746b30caa733b519f79fe372fd509bd6fc9fd#diff-81faa5084b795718e99cd0ade49cf78dc298c854cd443924412312256f01830d
func timeForSlackTimestamp(t string) time.Time {
	if t == "" {
		return time.Time{}
	}
	floatN, err := strconv.ParseFloat(string(t), 64)
	if err != nil {
		log.Println("ERROR parsing a JSONTimeString!", err)
		return time.Time{}
	}
	return time.Unix(int64(floatN), 0)
}

func compare(a, b []slack.Message) []slack.Message {
	for i := len(a) - 1; i >= 0; i-- {
		for _, vD := range b {
			if a[i].Timestamp == vD.Timestamp {
				a = append(a[:i], a[i+1:]...)
				break
			}
		}
	}
	return a
}

func buildMessageText(msg slack.Message) string {
	text := ""
	if len(msg.Blocks.BlockSet) > 0 {
		for i := 0; i < len(msg.Blocks.BlockSet); i++ {
			if msg.Blocks.BlockSet[i].BlockType() == slack.MBTRichText {
				for _, segment := range msg.Blocks.BlockSet[i].(*slack.RichTextBlock).Elements {
					if segment.RichTextElementType() == slack.RTESection {
						for _, piece := range segment.(*slack.RichTextSection).Elements {
							switch piece.RichTextSectionElementType() {
							case slack.RTSEText:
								formatSpecifier := ""
								te := piece.(*slack.RichTextSectionTextElement)
								if te.Style != nil {
									if te.Style.Bold {
										formatSpecifier = "**"
									}
									if te.Style.Italic {
										formatSpecifier = "*"
									}
									if te.Style.Strike {
										formatSpecifier = "~~"
									}
									if te.Style.Code {
										formatSpecifier = "`"
									}
								}
								text += formatSpecifier + piece.(*slack.RichTextSectionTextElement).Text + formatSpecifier
							case slack.RTSEEmoji:
								text += ":" + piece.(*slack.RichTextSectionEmojiElement).Name + ":"
							case slack.RTSEUser:
								uid := piece.(*slack.RichTextSectionUserElement).UserID
								text += fmt.Sprintf("`@%s (%s)`", users[uid].Name, users[uid].RealName)
							case slack.RTSELink:
								link := piece.(*slack.RichTextSectionLinkElement)
								if link.Text != "" {
									text += fmt.Sprintf("[%s](%s)", link.Text, link.URL)
								} else {
									text += fmt.Sprintf("[%s](%s)", link.URL, link.URL)
								}

							case slack.RTSEChannel:
								ch := piece.(*slack.RichTextSectionChannelElement)
								text += fmt.Sprintf("`#%s`", channels[ch.ChannelID])
							case slack.RTSEUserGroup:
								text += fmt.Sprintf("`@group:%s`", groups[piece.(*slack.RichTextSectionUserGroupElement).UsergroupID])
							case slack.RTSEColor:
								text += piece.(*slack.RichTextSectionColorElement).Value
							default:
								text += " !fixme <@719545123096231956>! "
							}
						}
					} else if segment.RichTextElementType() == slack.RTEList {
						data := segment.(*slack.RichTextUnknown).Raw
						var list RTEList
						json.Unmarshal([]byte(data), &list)
						for _, item := range list.Elements {
							for _, str := range item.Elements {
								text += "- " + str.Text + "\n"
							}
						}
						//todo... (and test!)
					}
				}
			}
			if msg.Blocks.BlockSet[i].BlockType() == slack.MBTImage {
				imb := msg.Blocks.BlockSet[i].(slack.ImageBlock)
				text += "\n"
				text += fmt.Sprintf("[%s ~ alt: %s](%s)", imb.Title.Text, imb.AltText, imb.ImageURL)
			}
		}
	} else {
		text = msg.Text
	}
	return text
}

func main() {
	confb, _ := os.ReadFile("config.json")
	json.Unmarshal(confb, &config)

	for name, tok := range config.SlackKeys {
		slack_clients[name] = slack.New(tok)
	}

	fmt.Printf("getting slack data (channels)\n")

	statClient, _ := webhook.NewWithURL(config.StatusHook)

	for name, url := range config.MirroredHooks {
		discord_clients[name], _ = webhook.NewWithURL(url)
	}

	db, _ := os.ReadFile("data/last_msg_set.json")
	json.Unmarshal(db, &slack_msgs)

	ub, _ := os.ReadFile("data/reference/users.json")
	json.Unmarshal(ub, &users)
	cb, _ := os.ReadFile("data/reference/channels.json")
	json.Unmarshal(cb, &channels)
	gb, _ := os.ReadFile("data/reference/usergroups.json")
	json.Unmarshal(gb, &groups)

	for name, chc := range config.ChannelConfigs {

		api := slack_clients[chc.Key]

		var msgs []slack.Message
		history, err := api.GetConversationHistory(&slack.GetConversationHistoryParameters{ChannelID: chc.Id})
		msgs = append(msgs, history.Messages...)

		if err != nil {
			fmt.Printf("[err] [slack] error reading data for channel %v (%v) ! Skipping.\n", name, chc.Id)
			statClient.CreateContent("D: Failure! ~ " + err.Error() + " ~ " + name)
		}

		diff := compare(msgs, slack_msgs[name])

		if len(diff) != 0 {
			for i := len(diff) - 1; i >= 0; i-- {
				msg := diff[i]

				if len(msg.Text) > 2000 {
					discord_clients[name].CreateContent("message skipped due to being too long. FIXME <@719545123096231956>!")
					continue
				}
				text := buildMessageText(msg)

				uName := "user not found in users.json"
				rxnString := ""
				if users[msg.User].Name != "" {
					uName = fmt.Sprintf("@%v (%v)", users[msg.User].Name, users[msg.User].RealName)
				}
				if len(msg.Reactions) > 0 {
					rxnString = "*reactions: "
					for _, rxn := range msg.Reactions {
						rxnString += fmt.Sprintf("[%v | %v]", rxn.Name, rxn.Count)
					}
					rxnString += "*"
				}

				filesInfo := ""
				for _, file := range msg.Files {
					filesInfo += "file: " + file.Name + " uploaded by " + users[file.User].Name + " (" + users[file.User].RealName + " )\n"
				}

				if filesInfo == "" {
					discord_clients[name].CreateEmbeds([]discord.Embed{discord.NewEmbedBuilder().
						SetAuthor(uName, "", users[msg.User].Profile.Image48).
						SetTimestamp(timeForSlackTimestamp(msg.Timestamp)).
						SetEmbedFooter(&discord.EmbedFooter{Text: rxnString}).
						SetFields(discord.EmbedField{
							Value: text,
						}).
						Build()},
					)
				} else {
					discord_clients[name].CreateEmbeds([]discord.Embed{discord.NewEmbedBuilder().
						SetAuthor(uName, "", users[msg.User].Profile.Image48).
						SetTimestamp(timeForSlackTimestamp(msg.Timestamp)).
						SetEmbedFooter(&discord.EmbedFooter{Text: rxnString}).
						SetFields(discord.EmbedField{
							Value: text,
						},
							discord.EmbedField{
								Value: filesInfo,
							}).
						Build()},
					)
				}
			}
		}
		slack_msgs[name] = msgs
		b, _ := json.Marshal(slack_msgs)
		os.WriteFile("data/last_msg_set.json", b, 0667)
	}

	statClient.CreateContent(":thumbsup: Slack data synced! `slacksync` Revsision: " + config.Revision)
}
