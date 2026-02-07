package GetMentions

import (
	"fmt"
	"slack-tag-summariser/Models"
	"time"

	"github.com/slack-go/slack"
)

type UniqueMention = Models.UniqueMention

var maxMentions = 30

func filterMentions(allMentions *slack.SearchMessages, userId string) ([]slack.SearchMessage, error) {
	// msg.Type should be 'message'
	// Not taking the devrev tickets
	// use the blocks -> rich_text -> rich_text_section -> type user to find for legitimate mention
	// msg.Channel.is_private should be false
	var filteredMentions []slack.SearchMessage

	threadsTaken := make(map[UniqueMention]struct{})

	for _, msg := range allMentions.Matches {
		if msg.Type != "message" ||
			msg.Channel.IsPrivate ||
			msg.Username == "devrev" {
			continue
		}

		for _, blk := range msg.Blocks.BlockSet {
			if blk.BlockType() != slack.MBTRichText {
				continue
			}

			richTextBlock, ok := blk.(*slack.RichTextBlock)

			if !ok {
				continue
			}

			for _, rtElem := range richTextBlock.Elements {
				if rtElem.RichTextElementType() != slack.RTESection {
					continue
				}
				richTextSection, ok2 := rtElem.(*slack.RichTextSection)

				if !ok2 {
					continue
				}

				for _, richTextSectionElem := range richTextSection.Elements {
					if richTextSectionElem.RichTextSectionElementType() != slack.RTSEUser {
						continue
					}

					//check the mentioned user
					richTextSectionUser, ok3 := richTextSectionElem.(*slack.RichTextSectionUserElement)

					if !ok3 {
						continue
					}

					if richTextSectionUser.UserID == userId {
						// this makes the current msg valid candidate for mention
						// now this should be only message we take for this thread
						uniqueKey := UniqueMention{
							Timestamp: msg.Timestamp,
							ChannelId: msg.Channel.ID,
						}
						if _, exists := threadsTaken[uniqueKey]; !exists {
							threadsTaken[uniqueKey] = struct{}{}
							filteredMentions = append(filteredMentions, msg)
							break
						}

					}
				}
				break
			}
			break
		}
	}

	return filteredMentions, nil
}

func GetMentions(slackClient *slack.Client, userId string) ([]slack.SearchMessage, error) {
	// prepare the query to search for messages mentioning the user in the last day
	yesterday := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	today := time.Now().Format("2006-01-02")
	query := fmt.Sprintf("<@%s> after:%s before:%s", userId, yesterday, today)

	params := slack.SearchParameters{
		Sort:          "timestamp",
		SortDirection: "desc",
		Count:         maxMentions, // taking at max 30 mentions in a day
	}
	// do the search api call
	res, err := slackClient.SearchMessages(query, params)

	if err != nil {
		return nil, err
	}

	//fmt.Println("total matches:", res.Total)
	//fmt.Println("total on the page1:", len(res.Matches))

	//total_mentions := res.Total
	//total_mentions_first_page := len(res.Matches)

	filteredMentions, err := filterMentions(res, userId)

	//accuracy := float64(len(filteredMentions)) / float64(res.Total) * 100.0

	return filteredMentions, nil
}
