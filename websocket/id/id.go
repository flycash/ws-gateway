package id

import "github.com/bwmarrin/snowflake"

type Generator interface {
	Generate() int64
}

type generator struct {
	node *snowflake.Node
}

func NewGenerator(nodeID int64) (Generator, error) {
	node, err := snowflake.NewNode(nodeID)
	if err != nil {
		return nil, err
	}
	return &generator{
		node: node,
	}, nil
}

func (g *generator) Generate() int64 {
	return g.node.Generate().Int64()
}
