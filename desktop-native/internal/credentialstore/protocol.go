package credentialstore

const Service = "com.hwj.easyagent.credentials.v2."
const LegacyService = "com.hwj.easyagent.native-preview"

type Request struct {
	Operation string
	Account   string
	Value     string
	Keychain  string // Empty in production; isolated keychain in integration tests.
}

type Response struct {
	Value string `json:",omitempty"`
	Found bool
	Error string `json:",omitempty"`
}
