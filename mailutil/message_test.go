package mailutil

import "testing"

func TestCopy(t *testing.T) {

	original := NewMessage()
	original.Header["Field"] = []string{"zero", "one"}
	original.Body = []byte("text")

	clone := original.Copy()

	// check clone

	if len(clone.Header["Field"]) != 2 || clone.Header["Field"][0] != "zero" || clone.Header["Field"][1] != "one" {
		t.Errorf("Error copying header")
	}

	if string(clone.Body) != "text" {
		t.Errorf("Error copying body")
	}

	// modify clone and check original

	clone.Body[0] = 'X'
	clone.Header["Field"][0] = "X" + clone.Header["Field"][0]
	clone.Header["Field"][1] = "X" + clone.Header["Field"][1]

	if len(original.Header["Field"]) != 2 || original.Header["Field"][0] != "zero" || original.Header["Field"][1] != "one" {
		t.Errorf("Header is not independent")
	}

	if string(original.Body) != "text" {
		t.Errorf("Body is not independent")
	}
}
