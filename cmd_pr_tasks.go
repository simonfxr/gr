package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/simonfxr/gr/pkg/provider"
	"github.com/simonfxr/gr/tables"
)

func handlePrTasks(info *provider.Info, cmd *PRTasksCmd) {
	if info == nil {
		fmt.Println("Cannot detect provider/repo info; aborting")
		return
	}
	ctx := context.Background()
	prNumber := cmd.Number
	if prNumber <= 0 {
		if prNumber = resolvePRNumberForBranch(ctx, info, cmd.Branch); prNumber <= 0 {
			return
		}
	}
	tasks, err := info.Provider.PrTasks(ctx, info, prNumber, provider.TaskListOptions{State: cmd.State})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	if cmd.JSON {
		b, _ := json.MarshalIndent(tasks, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(tasks) == 0 {
		fmt.Printf("No tasks for PR #%d.\n", prNumber)
		return
	}
	headers := []string{"ID", "Resolved", "Pending", "Creator", "When", "Comment", "Task"}
	tables.Render(headers, func(yield func([]string) bool) {
		for _, t := range tasks {
			when := ""
			if !t.CreatedAt.IsZero() {
				when = t.CreatedAt.Format(time.RFC3339)[:19]
			}
			comment := ""
			if t.CommentID > 0 {
				comment = strconv.Itoa(t.CommentID)
			}
			row := []string{strconv.Itoa(t.ID), strconv.FormatBool(t.Resolved), strconv.FormatBool(t.Pending),
				t.Creator, when, comment, t.Body}
			if !yield(row) {
				return
			}
		}
	})
}

func handlePrAddTask(info *provider.Info, cmd *PRAddTaskCmd) {
	if info == nil {
		fmt.Println("Cannot detect provider/repo info; aborting")
		return
	}
	ctx := context.Background()
	prNumber := cmd.Number
	if prNumber <= 0 {
		if prNumber = resolvePRNumberFromBranch(ctx, info); prNumber <= 0 {
			return
		}
	}
	task, err := info.Provider.PrAddTask(ctx, info, prNumber, provider.AddTaskOptions{
		Body:      cmd.Body,
		CommentID: cmd.CommentID,
		Pending:   cmd.Pending,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	if cmd.JSON {
		b, _ := json.MarshalIndent(task, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("Task %d added to PR #%d.\n", task.ID, prNumber)
}

func handlePrResolveTask(info *provider.Info, cmd *PRResolveTaskCmd) {
	if info == nil {
		fmt.Println("Cannot detect provider/repo info; aborting")
		return
	}
	ctx := context.Background()
	prNumber := cmd.Number
	if prNumber <= 0 {
		if prNumber = resolvePRNumberFromBranch(ctx, info); prNumber <= 0 {
			return
		}
	}
	task, err := info.Provider.PrSetTaskResolved(ctx, info, prNumber, cmd.TaskID, !cmd.Reopen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	if cmd.JSON {
		b, _ := json.MarshalIndent(task, "", "  ")
		fmt.Println(string(b))
		return
	}
	verb := "resolved"
	if cmd.Reopen {
		verb = "reopened"
	}
	fmt.Printf("Task %d on PR #%d %s.\n", cmd.TaskID, prNumber, verb)
}
