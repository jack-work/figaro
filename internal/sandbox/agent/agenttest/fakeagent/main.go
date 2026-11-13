package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jack-work/figaro/internal/sandbox/agent"
	"github.com/jack-work/figaro/internal/sandbox/agent/agenttest"
)

func main() {
	class := flag.String("class", "shell", "the one class it offers")
	count := flag.Int("count", 2, "how many sandboxes of it")
	workload := flag.String("workload", agent.Job, "the class's workload")
	flag.Parse()
	a, err := agenttest.Start("dev", agenttest.Spec{Name: *class, Workload: *workload, Count: *count})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer a.Close()
	fmt.Println(a.Endpoint)
	for i := range *count {
		name := fmt.Sprintf("%s%d", *class, i)
		fmt.Printf("%s %s\n", name, a.Workspace(name))
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}
