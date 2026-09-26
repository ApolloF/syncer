package steam

import (
	"io"

	"github.com/ApolloF/gamekit/vdf"
)

// Node is a parsed Valve KeyValues (VDF) object (gamekit/vdf).
type Node = vdf.Node

// ParseVDF reads text VDF (loginusers.vdf, localconfig.vdf, …). It is lenient:
// malformed input yields whatever was parsed up to that point.
func ParseVDF(r io.Reader) *Node { return vdf.Parse(r) }

func newNode() *Node { return &Node{Values: map[string]string{}, Children: map[string]*Node{}} }
