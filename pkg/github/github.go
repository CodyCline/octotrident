package github_ql

type Commit struct {
	OID *string `json:"oid"`
}
type ObjectResponse struct {
	Data struct {
		Repository map[string]Commit `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Message string `json:"message"`
}
