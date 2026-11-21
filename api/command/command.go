package command

type Kind string

const (
	Aria     Kind = "aria"
	Form     Kind = "form"
	Outfit   Kind = "outfit"
	Patch    Kind = "patch"
	Key      Kind = "key"
	Value    Kind = "value"
	Text     Kind = "text"
	Count    Kind = "count"
	Duration Kind = "duration"
	Path     Kind = "path"
	Word     Kind = "word"
	Verb     Kind = "verb"
)

type Source string

const (
	Arias       Source = "arias"
	AriaFlag    Source = "aria-flag"
	Attend      Source = "attend"
	Choices     Source = "choices"
	Commands    Source = "commands"
	Dirs        Source = "dirs"
	Export      Source = "export"
	Files       Source = "files"
	Forms       Source = "forms"
	Keys        Source = "keys"
	Providers   Source = "providers"
	Troupes     Source = "troupes"
	SetArgs     Source = "set"
	StateArgs   Source = "state"
	SendArgs    Source = "send"
	NewArgs     Source = "new"
	ForkArgs    Source = "fork"
	SucceedArgs Source = "succeed"
)

type Mode string

const (
	Routed  Mode = "routed"
	Hosted  Mode = "hosted"
	View    Mode = "view"
	Refused Mode = "refused"
)

func Kinds() []Kind {
	return []Kind{Aria, Form, Outfit, Patch, Key, Value, Text, Count, Duration, Path, Word, Verb}
}

func Sources() []Source {
	return []Source{Arias, AriaFlag, Attend, Choices, Commands, Dirs, Export, Files, Forms, Keys, Providers, Troupes, SetArgs, StateArgs, SendArgs, NewArgs, ForkArgs, SucceedArgs}
}

func Modes() []Mode { return []Mode{Routed, Hosted, View, Refused} }

type Choice struct {
	Name     string `json:"name"`
	Help     string `json:"help"`
	Keyboard bool   `json:"keyboard,omitempty"`
}

type Arg struct {
	Name     string   `json:"name"`
	Kind     Kind     `json:"kind"`
	Optional bool     `json:"optional,omitempty"`
	Repeats  bool     `json:"repeats,omitempty"`
	Choices  []Choice `json:"choices,omitempty"`
}

type Flag struct {
	Long     string   `json:"long"`
	Short    string   `json:"short,omitempty"`
	Value    Kind     `json:"value,omitempty"`
	Help     string   `json:"help"`
	Default  string   `json:"default,omitempty"`
	Subwords []string `json:"subwords,omitempty"`
	Repeats  bool     `json:"repeats,omitempty"`
}

type Command struct {
	Name         string   `json:"name"`
	Aliases      []string `json:"aliases,omitempty"`
	PagerAliases []string `json:"pagerAliases,omitempty"`
	Group        string   `json:"group,omitempty"`
	Help         string   `json:"help"`
	Usage        string   `json:"usage,omitempty"`
	Args         []Arg    `json:"args,omitempty"`
	Flags        []Flag   `json:"flags,omitempty"`
	Raw          bool     `json:"raw,omitempty"`
	Hidden       bool     `json:"hidden,omitempty"`
	PagerOnly    bool     `json:"pagerOnly,omitempty"`
	Complete     Source   `json:"complete,omitempty"`
	Pager        Mode     `json:"pager"`
	Refusal      string   `json:"refusal,omitempty"`
	Switches     bool     `json:"switches,omitempty"`
}

func (c Command) Bounds() (min, max int) {
	for _, a := range c.Args {
		if !a.Optional {
			min++
		}
		if a.Repeats {
			return min, 0
		}
	}
	return min, len(c.Args)
}

func (c Command) Choices() []Choice {
	if len(c.Args) == 0 {
		return nil
	}
	return c.Args[0].Choices
}

func (c Command) Keyboard(sub string) bool {
	for _, ch := range c.Choices() {
		if ch.Name == sub {
			return ch.Keyboard
		}
	}
	return false
}

func (c Command) Names() []string {
	return append([]string{c.Name}, c.Aliases...)
}

func All() []Command {
	out := make([]Command, len(table))
	for i, c := range table {
		if c.Pager == "" {
			c.Pager = Routed
		}
		out[i] = c
	}
	return out
}

func Shell(word string) (Command, bool) {
	for _, c := range All() {
		if c.PagerOnly {
			continue
		}
		for _, n := range c.Names() {
			if n == word {
				return c, true
			}
		}
	}
	return Command{}, false
}

func Pager(word string) (Command, bool) {
	all := All()
	for _, c := range all {
		if c.PagerOnly && (c.Name == word || contains(c.Aliases, word)) {
			return c, true
		}
	}
	for _, c := range all {
		if !c.PagerOnly && (contains(c.Names(), word) || contains(c.PagerAliases, word)) {
			return c, true
		}
	}
	return Command{}, false
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
