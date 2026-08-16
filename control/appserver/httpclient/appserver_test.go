package httpclient

import "testing"

func TestAppServerClientsIncludesTaskObservation(t *testing.T) {
	clients, err := AppServerClients(&Client{})
	if err != nil {
		t.Fatal(err)
	}
	if clients.Tasks == nil {
		t.Fatal("AppServer clients omit Task observation")
	}
	if err := clients.Validate(); err != nil {
		t.Fatal(err)
	}
}
