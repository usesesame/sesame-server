package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWaitForBackgroundJobsReturnsWhenJobsStop(t *testing.T) {
	var jobs sync.WaitGroup
	jobs.Add(1)
	go func() {
		defer jobs.Done()
		time.Sleep(10 * time.Millisecond)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !waitForBackgroundJobs(ctx, &jobs) {
		t.Fatal("waitForBackgroundJobs gave up while the jobs were still stopping")
	}
}

func TestWaitForBackgroundJobsStopsAtTheDeadline(t *testing.T) {
	var jobs sync.WaitGroup
	jobs.Add(1)
	defer jobs.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if waitForBackgroundJobs(ctx, &jobs) {
		t.Fatal("waitForBackgroundJobs reported completion while a job was still running")
	}
}
