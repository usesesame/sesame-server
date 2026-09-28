package migrations

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var migrationNamePattern = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

func duplicateMigrationNumbers(names []string) map[string][]string {
	numbers := map[string][]string{}
	for _, name := range names {
		match := migrationNamePattern.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		numbers[match[1]] = append(numbers[match[1]], name)
	}
	for number, files := range numbers {
		if len(files) < 2 {
			delete(numbers, number)
			continue
		}
		sort.Strings(files)
		numbers[number] = files
	}
	return numbers
}

func TestMigrationNumbersStayUniqueExceptTheRecordedPair(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read migration directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if migrationNamePattern.FindStringSubmatch(entry.Name()) == nil {
			t.Errorf("migration %s does not match NNNN_snake_case.sql", entry.Name())
			continue
		}
		names = append(names, entry.Name())
	}
	duplicates := duplicateMigrationNumbers(names)
	allowed := map[string][]string{
		"0023": {"0023_private_artifact_delivery.sql", "0023_release_owner_ring.sql"},
	}
	for number, files := range duplicates {
		want, ok := allowed[number]
		if !ok {
			t.Errorf("migration number %s is duplicated by %v", number, files)
			continue
		}
		if strings.Join(files, ",") != strings.Join(want, ",") {
			t.Errorf("migration number %s duplicates = %v, want the recorded pair %v", number, files, want)
		}
	}
	for _, file := range allowed["0023"] {
		found := false
		for _, name := range names {
			if name == file {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("recorded migration %s is missing; do not rename it", file)
		}
	}
	if len(duplicates) != 1 {
		t.Errorf("duplicate migration numbers = %v, want only the recorded 0023 pair", duplicates)
	}
}

func TestDuplicateMigrationNumbersDetectsANewDuplicate(t *testing.T) {
	duplicates := duplicateMigrationNumbers([]string{
		"0001_initial.sql",
		"0002_flags.sql",
		"0023_private_artifact_delivery.sql",
		"0023_release_owner_ring.sql",
		"0039_sync.sql",
		"0039_sync_again.sql",
		"notes.txt",
	})
	if len(duplicates) != 2 {
		t.Fatalf("duplicates = %v, want the 0023 pair and the new 0039 duplicate", duplicates)
	}
	if got := strings.Join(duplicates["0039"], ","); got != "0039_sync.sql,0039_sync_again.sql" {
		t.Fatalf("0039 duplicates = %s", got)
	}
	if got := strings.Join(duplicates["0023"], ","); got != "0023_private_artifact_delivery.sql,0023_release_owner_ring.sql" {
		t.Fatalf("0023 duplicates = %s", got)
	}
}
