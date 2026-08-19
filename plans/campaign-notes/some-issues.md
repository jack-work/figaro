First -- the bash is not supposed to come in like this.

- This content is what is logged after some tools are complete
- The checkmark should be shown alone after the work is done. We get the loading spinner instead, which
  should have been erased by the CLI.

─── ⠋ ▶ bash · cd /home/gluck/dev/figaro-qua/main && sed -n '2285,2310p' cmd/figaro/main.go && echo "---" && sed -n '1185,1200p' cmd/figaro/main.go ───
func buildChalkboard() \*template.Template {
tmpls, err := chalkboard.LoadDefaultTemplates()
if err != nil {
slog.Warn("chalkboard templates load failed (disabled)", "err", err)
return nil
}
home, _ := os.UserHomeDir()
overrideDir := filepath.Join(home, ".config", "figaro", "chalkboard")
if \_, err := os.Stat(overrideDir); err == nil {
if t, err := chalkboard.LoadOverrideTemplates(tmpls, overrideDir); err == nil {
tmpls = t
} else {
slog.Warn("chalkboard override templates (using defaults)", "err", err)
}
}
return tmpls
}

func stateDir() string {
home, _ := os.UserHomeDir()
return filepath.Join(home, ".local", "state", "figaro")
}

## func extractPrompt(args []string) string { for i, arg := range args { if arg == "--" {

```
if !r.Found {
	die("no figaro bound to this shell")
}
figaroID := r.FigaroID

home, _ := os.UserHomeDir()
ariaPath := filepath.Join(home, ".local", "state", "figaro", "arias", figaroID, "aria.jsonl")
fs, err := store.OpenFileStream[message.Message](ariaPath)
if err != nil {
	die("open aria: %s", err)
}
entries := fs.Durable()
if len(entries) == 0 {
	fmt.Fprintln(os.Stderr, "(empty aria)")
	return
```

# This one seems like it was correct

─── ✓ ▶ bash · cd /home/gluck/dev/figaro-qua/main && sed -n '2285,2310p' cmd/figaro/main.go && echo "---" && sed -n '1185,1200p' cmd/figaro/main.go ───
───

# Furthermore, execution paused for a very long time at this point.

Now I have the complete picture. Let me write the document.

______________________________________________________________________

- I can see that this is figaro logical time [#28] (tool_use)
- After the message comes in, the tool request should have been immediately rendered.
- Instead, we had to wait a very long time before the next message came in.
- I think we should emit logs to otel that include the logical time and also every time
  we fan out an event. I need to get to the bottom of this.
