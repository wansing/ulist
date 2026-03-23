package html

import (
	"net/http"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// supported languages
var matcher = language.NewMatcher([]language.Tag{
	language.Make("en-US"), // The first language is used as fallback.
	language.Make("de-DE"),
})

type Lang language.Tag

func MakeLang(r *http.Request) Lang {
	tag, _ := language.MatchStrings(matcher, r.Header.Get("Accept-Language"))
	return Lang(tag)
}

func (l Lang) Sort(strs []string) []string {
	collator := collate.New(language.Tag(l), collate.IgnoreCase) // collator is not thread-safe btw
	collator.SortStrings(strs)
	return strs
}

func (l Lang) Tr(key message.Reference, a ...any) string {
	return message.NewPrinter(language.Tag(l)).Sprintf(key, a...)
}
