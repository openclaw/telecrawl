package postbox

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

const (
	messageMetadataServiceAction = -1132984447
	messageMetadataLocation      = -1138242673
	messageMetadataPoll          = -165764138
	messageMetadataWebpage       = -661322156
)

var urlPattern = regexp.MustCompile(`https?://[^\s<>()"']+`)

func AttachMessageMetadata(messages []MessageRecord) int {
	count := 0
	for i := range messages {
		metadataType, title, url, rawJSON := messageMetadata(messages[i])
		if metadataType == "" {
			continue
		}
		messages[i].MetadataType = metadataType
		messages[i].MetadataTitle = title
		messages[i].MetadataURL = url
		messages[i].MetadataJSON = rawJSON
		count++
	}
	return count
}

func messageMetadata(msg MessageRecord) (metadataType, title, url, rawJSON string) {
	for _, item := range msg.EmbeddedMedia {
		media, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typeHash, ok := int64Value(media["@type"])
		if !ok {
			continue
		}
		switch typeHash {
		case messageMetadataWebpage:
			return "web_page", firstString(media, "ti", "title", "t", "tx"), firstString(media, "u", "du", "url"), metadataJSON(media)
		case messageMetadataLocation:
			return "location", firstString(media, "adr", "t", "title"), "", metadataJSON(media)
		case messageMetadataPoll:
			return "poll", firstString(media, "t", "title"), "", metadataJSON(media)
		case messageMetadataServiceAction:
			return "service_action", serviceActionTitle(media), "", metadataJSON(media)
		}
	}
	if msg.MediaType == "web_page" {
		if url := firstURL(msg.Text); url != "" {
			return "web_page", "", url, metadataJSON(map[string]any{"url": url})
		}
	}
	return "", "", "", ""
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := cleanText(value[key]); text != "" {
			return text
		}
	}
	return ""
}

func firstURL(text string) string {
	match := urlPattern.FindString(text)
	return strings.TrimRight(match, ".,;:")
}

func serviceActionTitle(value map[string]any) string {
	for _, key := range []string{"d", "dr", "vc"} {
		if _, ok := value[key]; ok {
			return "call"
		}
	}
	if _, ok := value["t"]; ok {
		return "title_change"
	}
	if _, ok := value["m"]; ok {
		return "member_change"
	}
	if _, ok := value["p"]; ok {
		return "pin"
	}
	return "service_action"
}

func metadataJSON(value any) string {
	data, err := json.Marshal(jsonSafe(value))
	if err != nil {
		return ""
	}
	return string(data)
}

func jsonSafe(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if key != objectOrderKey {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		out := make(map[string]any, len(keys))
		for _, key := range keys {
			out[key] = jsonSafe(typed[key])
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, value := range typed {
			out[i] = jsonSafe(value)
		}
		return out
	case []byte:
		return map[string]string{"base64": base64.StdEncoding.EncodeToString(typed)}
	default:
		return typed
	}
}
