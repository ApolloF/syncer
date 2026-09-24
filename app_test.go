package main

import "testing"

func TestNormalizeID(t *testing.T) {
	good := "ABCDEFG-HIJKLMN-OPQRSTU-VWXYZ23-4567ABC-DEFGHIJ-KLMNOPQ-RSTUVWX"
	for _, in := range []string{good, "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrstuvwx", " ABCDEFG HIJKLMN-OPQRSTU-VWXYZ23-4567ABC-DEFGHIJ-KLMNOPQ-RSTUVWX\n"} {
		got, err := normalizeID(in)
		if err != nil || got != good {
			t.Errorf("normalizeID(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ABC", good + "A", "ABCDEF1-HIJKLMN-OPQRSTU-VWXYZ23-4567ABC-DEFGHIJ-KLMNOPQ-RSTUVWX"} {
		if _, err := normalizeID(bad); err == nil {
			t.Errorf("normalizeID(%q) should fail", bad)
		}
	}
}
