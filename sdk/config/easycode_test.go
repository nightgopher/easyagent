package config

import "testing"

func TestEasyCodeDefaultDoesNotOverwritePreviousProvider(t *testing.T) {
	dir := t.TempDir()
	if err := SaveRuntimeOverrideDefaultModel(dir, "openai", "previous-model"); err != nil {
		t.Fatal(err)
	}
	if err := SaveEasyCodeDefaultModel(dir, "account-model"); err != nil {
		t.Fatal(err)
	}
	deepv := Default()
	deepv.Provider = "deepv"
	deepv.OpenAIModel = "initial"
	deepv.LoadRuntimeOverrides(dir)
	if deepv.Provider != "deepv" || deepv.OpenAIModel != "account-model" {
		t.Fatalf("lost account model: %+v", deepv)
	}
	previous := Default()
	previous.Provider = "openai"
	previous.OpenAIModel = "initial"
	previous.LoadRuntimeOverrides(dir)
	if previous.Provider != "openai" || previous.OpenAIModel != "previous-model" {
		t.Fatalf("lost previous model: %+v", previous)
	}
}
