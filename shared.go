package asevre

import "image"

// Tile represents a single tile in the game.
type Tile struct {
	Width, Height, ID, TilesetID int
	XFlip, YFlip, DiagonalFlip   bool
	X, Y                         float64
	Properties                   map[string]string // Tags like "solid", "hazard", etc.
	Image                        image.Image
}

// TileSet represents a collection of tiles.
type TileSet struct {
	Tiles [][]Tile
}

// TileMap represents a single frame of animation or state.
type TileMap struct {
	Tiles        [][]Tile
	FlipX, FlipY bool
}
