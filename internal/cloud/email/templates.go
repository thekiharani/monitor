package email

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"strings"
	"sync/atomic"
	texttemplate "text/template"
	"time"
)

//go:embed templates/*.html
var htmlFiles embed.FS

//go:embed templates/*.txt
var textFiles embed.FS

type LoginCodeData struct {
	Code      string
	ExpiresIn string
}

type EmailChangeCodeData struct {
	Code      string
	ExpiresIn string
	Current   string
}

type EmailChangedData struct {
	Previous string
	Current  string
}

type Brand struct {
	AppOrigin string
	Year      int
}

var brand atomic.Pointer[Brand]

func defaultBrand() Brand {
	return Brand{
		AppOrigin: "https://monitor.noria.co.ke",
		Year:      time.Now().Year(),
	}
}

func Configure(appOrigin string) {
	origin := strings.TrimRight(appOrigin, "/")
	if origin == "" {
		origin = defaultBrand().AppOrigin
	}
	brand.Store(&Brand{
		AppOrigin: origin,
		Year:      time.Now().Year(),
	})
}

func currentBrand() Brand {
	if b := brand.Load(); b != nil {
		return Brand{AppOrigin: b.AppOrigin, Year: time.Now().Year()}
	}
	return defaultBrand()
}

type view struct {
	Brand Brand
	Data  any
}

type ButtonData struct {
	Label string
	URL   any
}

type tone struct {
	Bar, PillBg, PillInk, Ink string
}

var tones = map[string]tone{
	"crit": {Bar: "#d92d20", PillBg: "#fde8e7", PillInk: "#b42318", Ink: "#b42318"},
	"warn": {Bar: "#f79009", PillBg: "#fef0c7", PillInk: "#b54708", Ink: "#b54708"},
	"ok":   {Bar: "#12b76a", PillBg: "#dcfae6", PillInk: "#067647", Ink: "#067647"},
	"info": {Bar: "#0080ac", PillBg: "#e0f2fa", PillInk: "#0b6a8d", Ink: "#0f1317"},
}

func toneFor(name string) tone {
	if t, ok := tones[name]; ok {
		return t
	}
	return tones["info"]
}

type IntroData struct {
	Tone, PillBg, PillInk, Label, Title, Summary string
}

type Fact struct {
	Label, Value, Tone, Color string
}

var templateFuncs = template.FuncMap{
	"button": func(label string, url any) ButtonData {
		return ButtonData{Label: label, URL: url}
	},
	"bar": func(name string) string { return toneFor(name).Bar },
	"intro": func(name, label, title, summary string) IntroData {
		t := toneFor(name)
		return IntroData{Tone: name, PillBg: t.PillBg, PillInk: t.PillInk, Label: label, Title: title, Summary: summary}
	},
	"facts": func(triples ...any) []Fact {
		var out []Fact
		for i := 0; i+2 < len(triples); i += 3 {
			value := fmt.Sprint(triples[i+1])
			if triples[i+1] == nil || value == "" || value == "<nil>" {
				continue
			}
			name := fmt.Sprint(triples[i+2])
			color := "#0f1317"
			if name != "" {
				color = toneFor(name).Ink
			}
			out = append(out, Fact{Label: fmt.Sprint(triples[i]), Value: value, Tone: name, Color: color})
		}
		return out
	},
}

var htmlTemplates = parseHTML()
var textTemplates = texttemplate.Must(texttemplate.ParseFS(textFiles, "templates/*.txt"))

func parseHTML() map[string]*template.Template {
	entries, err := fs.Glob(htmlFiles, "templates/*.html")
	if err != nil {
		panic(err)
	}
	parsed := map[string]*template.Template{}
	for _, entry := range entries {
		name := strings.TrimSuffix(strings.TrimPrefix(entry, "templates/"), ".html")
		if name == "layout" {
			continue
		}
		parsed[name] = template.Must(
			template.New("layout.html").Funcs(templateFuncs).ParseFS(htmlFiles, "templates/layout.html", entry),
		)
	}
	return parsed
}

var subjects = map[string]string{
	"login_code":                 "Your Noria Monitor login code",
	"payment_failed":             "Your Noria Monitor payment did not go through",
	"subscription_canceled":      "Your Noria Monitor subscription has been canceled",
	"subscription_expiring_card": "Your card on file expires soon",
	"billing_renewal":            "Your {{.plan_name}} plan renews soon",
	"team_invite":                "You have been invited to a Noria Monitor workspace",
	"email_change_code":          "Confirm your new Noria Monitor email address",
	"email_changed":              "Your Noria Monitor sign-in address was changed",
	"monitor_alert":              "{{.headline}}",
	"probe_status":               "{{.headline}}",
}

func Render(name string, data any) (Message, error) {
	subjectTemplate, ok := subjects[name]
	if !ok {
		return Message{}, fmt.Errorf("unknown email template %q", name)
	}
	htmlTemplate, ok := htmlTemplates[name]
	if !ok {
		return Message{}, fmt.Errorf("unknown email template %q", name)
	}

	page := view{Brand: currentBrand(), Data: data}

	subject, err := renderSubject(name, subjectTemplate, data)
	if err != nil {
		return Message{}, err
	}
	var html, text strings.Builder
	if err := htmlTemplate.ExecuteTemplate(&html, "layout.html", page); err != nil {
		return Message{}, err
	}
	if err := textTemplates.ExecuteTemplate(&text, name+".txt", page); err != nil {
		return Message{}, err
	}
	return Message{Subject: subject, Text: text.String(), HTML: html.String()}, nil
}

func renderSubject(name, pattern string, data any) (string, error) {
	if !strings.Contains(pattern, "{{") {
		return pattern, nil
	}
	parsed, err := texttemplate.New(name + ".subject").Parse(pattern)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := parsed.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func HumanDuration(value time.Duration) string {
	minutes := int(value / time.Minute)
	if minutes > 0 && value%time.Minute == 0 {
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	return value.String()
}
