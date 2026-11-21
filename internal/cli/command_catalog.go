package cli

import (
	"github.com/jack-work/figaro/api/command"
	"github.com/jack-work/figaro/internal/cmdkit"
)

func verb(name string, c *cmdkit.Command) *cmdkit.Command {
	spec, ok := command.Shell(name)
	if !ok || spec.Name != name {
		panic("cli: " + name + " has no entry in api/command")
	}
	describe(c, spec)
	flags := flagDefs(spec.Flags)
	if spec.Raw {
		c.CompleteFlags = flags
	} else {
		c.Flags = flags
		c.ArgsMin, c.ArgsMax = spec.Bounds()
	}
	if complete, ok := completers[spec.Complete]; ok {
		c.CompleteArgs = complete(spec)
	}
	return c
}

func describe(c *cmdkit.Command, spec command.Command) {
	c.Name, c.Aliases, c.Group, c.Short, c.Usage = spec.Name, spec.Aliases, spec.Group, spec.Help, spec.Usage
	c.Hidden, c.PassRaw = spec.Hidden, spec.Raw
}

func describeHelp(r *cmdkit.Router) {
	help, _ := r.Command("help")
	spec, _ := command.Shell("help")
	describe(help, spec)
	help.ArgsMin, help.ArgsMax = spec.Bounds()
}

func flagDefs(flags []command.Flag) []cmdkit.FlagDef {
	if len(flags) == 0 {
		return nil
	}
	out := make([]cmdkit.FlagDef, len(flags))
	for i, f := range flags {
		out[i] = cmdkit.FlagDef{Long: f.Long, Short: f.Short, Description: f.Help, IsBool: f.Value == "", Default: f.Default, Subwords: f.Subwords, Repeats: f.Repeats}
	}
	return out
}

type completeFunc = func(*cmdkit.CompleteContext) []string

func fixed(f completeFunc) func(command.Command) completeFunc {
	return func(command.Command) completeFunc { return f }
}

func wordsThen(rest completeFunc) func(command.Command) completeFunc {
	return func(spec command.Command) completeFunc {
		var words [][2]string
		for _, ch := range spec.Choices() {
			words = append(words, [2]string{ch.Name, ch.Help})
		}
		return completeWords(words, rest)
	}
}

var completers = map[command.Source]func(command.Command) completeFunc{
	command.Arias:       fixed(completeAriaIDsPositionalOrFlag),
	command.AriaFlag:    fixed(completeAriaIDsAfterFlag(nil)),
	command.Keys:        fixed(completeAriaIDsAfterFlag(completeFormKeys)),
	command.Attend:      fixed(completeAttend),
	command.Choices:     wordsThen(nil),
	command.Providers:   wordsThen(completeProviderAfterVerb),
	command.Troupes:     wordsThen(completeTroupeAfterLogin),
	command.Dirs:        fixed(completeCdDirs),
	command.Export:      fixed(completeExport),
	command.Files:       fixed(completeFileArg),
	command.Forms:       fixed(completeFormVerb),
	command.SetArgs:     fixed(completeSetArgs),
	command.StateArgs:   fixed(completeStateArgs),
	command.SendArgs:    fixed(completeSend),
	command.NewArgs:     fixed(completeNewPrompt),
	command.ForkArgs:    fixed(completeForkPrompt),
	command.SucceedArgs: fixed(completeSucceed),
}
