package product

import (
	"encoding/json"
	"testing"
)

func TestPlansMatchThePublishedCatalog(t *testing.T) {
	type expectedPlan struct {
		id          string
		name        string
		price       string
		annualPrice string
		billing     string
		available   bool
	}
	expected := []expectedPlan{
		{id: "free", name: "Sesame", price: "0", billing: "none", available: true},
		{id: "sync", name: "Sesame Sync", price: "1.00", annualPrice: "10.00", billing: "monthly", available: false},
	}
	plans := Plans()
	if len(plans) != len(expected) {
		t.Fatalf("Plans() returned %d plans, want %d", len(plans), len(expected))
	}
	seen := map[string]bool{}
	for index, plan := range plans {
		want := expected[index]
		if plan.ID != want.id || plan.Name != want.name || plan.Price != want.price || plan.AnnualPrice != want.annualPrice || plan.Billing != want.billing || plan.Available != want.available {
			t.Fatalf("plan %d = %+v, want %+v", index, plan, want)
		}
		if seen[plan.ID] {
			t.Fatalf("plan id %q appears twice", plan.ID)
		}
		seen[plan.ID] = true
		if plan.Description == "" {
			t.Fatalf("plan %q has no description", plan.ID)
		}
		if len(plan.Includes) == 0 {
			t.Fatalf("plan %q lists no includes", plan.ID)
		}
		for _, include := range plan.Includes {
			if include == "" {
				t.Fatalf("plan %q lists an empty include", plan.ID)
			}
		}
	}
	if !seen["free"] || !seen["sync"] {
		t.Fatalf("catalog plans = %v, want the free and sync plans", seen)
	}
	plans[0].Name = "mutated"
	if Plans()[0].Name != "Sesame" {
		t.Fatal("mutating a returned plan changed the catalog")
	}
}

func TestPlanJSONOmitsUnsetAnnualPrice(t *testing.T) {
	encoded, err := json.Marshal(Plans())
	if err != nil {
		t.Fatalf("marshal plans: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode plans: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded %d plans, want 2", len(decoded))
	}
	free, sync := decoded[0], decoded[1]
	if _, present := free["annualPrice"]; present {
		t.Fatalf("free plan carries an annual price: %v", free["annualPrice"])
	}
	if sync["annualPrice"] != "10.00" {
		t.Fatalf("sync annual price = %v, want 10.00", sync["annualPrice"])
	}
	for _, plan := range decoded {
		for _, key := range []string{"id", "name", "price", "billing", "available", "description", "includes"} {
			if _, present := plan[key]; !present {
				t.Fatalf("plan %v misses the %q field", plan["id"], key)
			}
		}
	}
}

func TestUnpublishedPlatformReleasesStayUnavailable(t *testing.T) {
	cases := []struct {
		platform string
		release  Release
	}{
		{platform: "windows", release: LatestWindowsRelease()},
		{platform: "linux", release: LatestLinuxRelease()},
	}
	for _, testCase := range cases {
		release := testCase.release
		if release.Platform != testCase.platform {
			t.Fatalf("%s release platform = %q", testCase.platform, release.Platform)
		}
		if release.Available || release.Signed {
			t.Fatalf("%s release = %+v, want unavailable and unsigned", testCase.platform, release)
		}
		if release.Version != "" || release.URL != "" || release.SHA256 != "" {
			t.Fatalf("%s release publishes artifact details: %+v", testCase.platform, release)
		}
		if release.Channel == "" || release.Message == "" {
			t.Fatalf("%s release lacks channel or message: %+v", testCase.platform, release)
		}
	}
}
