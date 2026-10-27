package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/simonfxr/gr/pkg/provider"
	"github.com/simonfxr/gr/tables"
)

func prTasks(ctx context.Context, info *provider.Info, cmd *PRTasksCmd) error {
	number, err := resolvePR(ctx, info, "", cmd.PRSelector)
	if err != nil {
		return err
	}
	tasks, err := info.Provider.PrTasks(ctx, info, number, provider.TaskListOptions{State: cmd.State})
	if err != nil {
		return err
	}
	if cmd.JSON {
		return printJSON(tasks)
	}
	if len(tasks) == 0 {
		fmt.Printf("No tasks for PR #%d.\n", number)
		return nil
	}
	headers := []string{"ID", "Resolved", "Pending", "Creator", "When", "Comment", "Task"}
	tables.Render(headers, func(yield func([]string) bool) {
		for _, t := range tasks {
			comment := ""
			if t.CommentID > 0 {
				comment = strconv.Itoa(t.CommentID)
			}
			row := []string{strconv.Itoa(t.ID), strconv.FormatBool(t.Resolved), strconv.FormatBool(t.Pending),
				t.Creator, formatTime(t.CreatedAt), comment, t.Body}
			if !yield(row) {
				return
			}
		}
	})
	return nil
}

func prAddTask(ctx context.Context, info *provider.Info, cmd *PRAddTaskCmd) error {
	body, err := resolveBody(cmd.Body, cmd.BodyFile, "task text")
	if err != nil {
		return err
	}
	number, err := resolvePR(ctx, info, "", cmd.PRSelector)
	if err != nil {
		return err
	}
	task, err := info.Provider.PrAddTask(ctx, info, number, provider.AddTaskOptions{
		Body:      body,
		CommentID: cmd.CommentID,
		Pending:   cmd.Pending,
	})
	if err != nil {
		return err
	}
	if cmd.JSON {
		return printJSON(task)
	}
	fmt.Printf("Task %d added to PR #%d.\n", task.ID, number)
	return nil
}

func prResolveTask(ctx context.Context, info *provider.Info, cmd *PRResolveTaskCmd) error {
	number, err := resolvePR(ctx, info, "", cmd.PRSelector)
	if err != nil {
		return err
	}
	task, err := info.Provider.PrSetTaskResolved(ctx, info, number, cmd.TaskID, !cmd.Reopen)
	if err != nil {
		return err
	}
	if cmd.JSON {
		return printJSON(task)
	}
	verb := "resolved"
	if cmd.Reopen {
		verb = "reopened"
	}
	fmt.Printf("Task %d on PR #%d %s.\n", cmd.TaskID, number, verb)
	return nil
}
