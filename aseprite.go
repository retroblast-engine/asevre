package asevre

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
)

// AsepriteSprite represents a parsed Aseprite file.
type AsepriteSprite struct {
	TileSet TileSet
	States  map[string][]TileMap // Maps state names to their tilemaps
}

type Sprites struct {
	Current *ebiten.Image
	All     []*ebiten.Image
}
type Color struct {
	Red   BYTE
	Green BYTE
	Blue  BYTE
}

type Packet struct {
	NumberOfPalEntriesToSkipFromTheLastPacket BYTE // Number of palette entries to skip from the last packet (start from 0)
	NumberOfColorsInThisPacket                BYTE // Number of colors in this packet (0 means 256 colors)
	Colors                                    []Color
}

type Chunk0x0004 struct {
	NumberOfPackets WORD // Number of packets in this chunk
	Packets         []Packet
}

func parseChunk0x0004(data []byte) (*Chunk0x0004, error) {
	reader := bytes.NewReader(data)
	var chunk Chunk0x0004

	if err := binary.Read(reader, binary.LittleEndian, &chunk.NumberOfPackets); err != nil {
		return nil, err
	}

	chunk.Packets = make([]Packet, chunk.NumberOfPackets)
	for i := 0; i < int(chunk.NumberOfPackets); i++ {
		packet := &chunk.Packets[i]

		if err := binary.Read(reader, binary.LittleEndian, &packet.NumberOfPalEntriesToSkipFromTheLastPacket); err != nil {
			return nil, err
		}
		if err := binary.Read(reader, binary.LittleEndian, &packet.NumberOfColorsInThisPacket); err != nil {
			return nil, err
		}

		packet.Colors = make([]Color, int(packet.NumberOfColorsInThisPacket))
		for j := 0; j < int(packet.NumberOfColorsInThisPacket); j++ {
			if err := binary.Read(reader, binary.LittleEndian, &packet.Colors[j]); err != nil {
				return nil, err
			}
		}
	}

	return &chunk, nil
}

/* Tileset flags (1: Enabled, 0: Disabled)
Bit 5 (32) - Same for D(iagonal) flips
Bit 4 (16) - Same for Y flips
Bit 3 (8)  - Aseprite will try to match modified tiles with their X flipped version automatically in Auto mode when using this tileset.
Bit 2 (4)  - Tilemaps using this tileset use tile ID=0 as empty tile (this is the new format). In rare cases this bit is off, and the empty tile will be equal to 0xffffffff (used in internal versions of Aseprite)
Bit 1 (2)  - Include tiles inside this file
Bit 0 (1)  - Include link to external file
*/

// Define constants for each flag
const (
	FlagIncludeLinkToExternalFile = 1 << iota // 1
	FlagIncludeTilesInsideFile                // 2
	FlagTileIDZeroAsEmptyTile                 // 4
	FlagXFlipAutoMatch                        // 8
	FlagYFlipAutoMatch                        // 16
	FlagDiagonalFlipAutoMatch                 // 32
)

// Define the STRING type
type STRING struct {
	Length WORD   // string length (number of bytes) // 2 bytes
	Chars  []BYTE // characters (in UTF-8)
}

type Chunk2003 struct {
	TilesetID              DWORD    // Tileset ID (4 bytes) // 4 bytes so far
	TilesetFlags           DWORD    // Tileset flags (4 bytes) // 8 bytes so far
	NumberOfTiles          DWORD    // Number of tiles (4 bytes) // 12 bytes so far
	TileWidth              WORD     // Tile width in pixels (2 bytes) // 14 bytes so far
	TileHeight             WORD     // Tile height in pixels (2 bytes) // 16 bytes so far
	BaseIndex              SHORT    // Base index (2 bytes) just for UI purposes // 18 bytes so far
	Reserved               [14]BYTE // Reserved for future use, set to zero (14 bytes) // 32 bytes so far
	TilesetName            STRING   // Tileset name (variable length) // 34 bytes so far + variable length
	SizeOfTilesetImage     DWORD    // Data length of the tileset image data (4 bytes) // 38 bytes so far + variable string chars length
	CompressedTilesetImage []byte   // Compressed tileset image data (variable length)
}

func parseChunk0x2023(data []byte) (*Chunk2003, error) {
	r := bytes.NewReader(data)

	chunk := &Chunk2003{}
	if err := binary.Read(r, binary.LittleEndian, &chunk.TilesetID); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.TilesetFlags); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.NumberOfTiles); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.TileWidth); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.TileHeight); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.BaseIndex); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.Reserved); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.TilesetName.Length); err != nil {
		return nil, err
	}
	chunk.TilesetName.Chars = make([]BYTE, chunk.TilesetName.Length)
	if err := binary.Read(r, binary.LittleEndian, &chunk.TilesetName.Chars); err != nil {
		return nil, err
	}

	flags := chunk.GetTilesetFlags()
	if flags.IncludeTilesInsideFile {
		if err := binary.Read(r, binary.LittleEndian, &chunk.SizeOfTilesetImage); err != nil {
			return nil, err
		}
		// fmt.Printf("Size of Tileset Image: %d\n", chunk.SizeOfTilesetImage)

		sizeSoFar := 34 + len(chunk.TilesetName.Chars) + 4

		chunk.CompressedTilesetImage = make([]byte, len(data)-sizeSoFar)
		if err := binary.Read(r, binary.LittleEndian, &chunk.CompressedTilesetImage); err != nil {
			return nil, err
		}

		decompressed, err := decompressZlib(chunk.CompressedTilesetImage)
		if err != nil {
			return nil, fmt.Errorf("error decompressing Tileset Image data: %v", err)
		}

		// Loop through []byte decompressed data to read PIXEL data
		// Create a new RGBA image
		// count := 0

		// img := image.NewRGBA(image.Rect(0, 0, 11*8, 8)) // TODO: Hardcoded values!!!!!!!!!!!!!!!!

		// Αυτα ειναι ΙΔΙΑ (704 pixels)
		// Οπου το pixel ειναι ου

		// How many []PIXEL do we have?
		// Answer: (Tile Width) x (Tile Height x Number of Tiles)
		tileWidth := int(chunk.TileWidth)
		tileHeight := int(chunk.TileHeight)
		// numTiles := int(chunk.NumberOfTiles)
		// numPixels := tileWidth * tileHeight * numTiles

		// fmt.Println("Number of PIXEL[] expected:", numPixels)
		// fmt.Println("Decompressed Tileset Image Data Length:", len(decompressed), "bytes")

		var tilesetTiles []byte

		// Assuming tileWidth and tileHeight are defined
		tileSize := tileWidth * tileHeight

		// Loop through the decompressed data to extract each tile
		for i := 0; i < len(decompressed); i += tileSize {
			// Ensure we don't go out of bounds
			if i+tileSize > len(decompressed) {
				break
			}

			// Extract the current tile
			tile := decompressed[i : i+tileSize]

			// Append the current tile to the tilesetTile slice
			tilesetTiles = append(tilesetTiles, tile...)
		}

		// for tile := 0; tile < numTiles; tile++ {
		// 	printTile(tile, tilesetTiles, tileWidth, tileHeight)
		// 	fmt.Println()
		// 	fmt.Println()
		// }

		// fmt.Println()

		// printTilesHorizontally(tilesetTiles, tileWidth, tileHeight)

		// Save the image to disk
		// file, err := os.Create("output.png")
		// if err != nil {
		// 	fmt.Println("Error creating file:", err)
		// 	return nil, err
		// }
		// defer file.Close()

		// err = png.Encode(file, img)
		// if err != nil {
		// 	fmt.Println("Error encoding PNG:", err)
		// 	return nil, err
		// }

		// fmt.Println("Image saved to output.png")

	}

	return chunk, nil
}

func printTilesHorizontally(tilesetTiles []byte, tileWidth, tileHeight int) {
	tileSize := tileWidth * tileHeight
	numTiles := len(tilesetTiles) / tileSize

	for row := 0; row < tileHeight; row++ {
		for tile := 0; tile < numTiles; tile++ {
			start := tile*tileSize + row*tileWidth
			end := start + tileWidth
			for i := start; i < end; i++ {
				fmt.Printf("%d ", tilesetTiles[i])
			}
		}
		fmt.Println()
	}
}

func printTile(tileNumber int, tilesetTiles []byte, tileWidth, tileHeight int) {
	tileSize := tileWidth * tileHeight
	start := tileNumber * tileSize
	end := start + tileSize

	// Ensure the end index does not exceed the length of the tilesetTiles data
	if end > len(tilesetTiles) {
		fmt.Println("Tile number out of range")
		return
	}

	// Extract the tile
	tile := tilesetTiles[start:end]

	// Print the tile in a readable format
	for i := 0; i < tileHeight; i++ {
		for j := 0; j < tileWidth; j++ {
			fmt.Printf("%x ", tile[i*tileWidth+j])
		}
		fmt.Println()
	}
}

// Define the PIXEL type
type PIXEL struct {
	RGBA      [4]BYTE // BYTE[4], each pixel has 4 bytes in this order: Red, Green, Blue, Alpha
	Grayscale [2]BYTE // BYTE[2], each pixel has 2 bytes in this order: Value, Alpha
	Indexed   BYTE    // BYTE, each pixel uses 1 byte (the index)
}

type CompressedTilesetImageData struct {
	Length DWORD
	Image  []BYTE
}

type TilesetFlags struct {
	IncludeLinkToExternalFile bool
	IncludeTilesInsideFile    bool
	TileIDZeroAsEmptyTile     bool
	XFlipAutoMatch            bool
	YFlipAutoMatch            bool
	DiagonalFlipAutoMatch     bool
}

// Method to determine which flags are enabled
func (c *Chunk2003) GetTilesetFlags() TilesetFlags {
	return TilesetFlags{
		IncludeLinkToExternalFile: c.TilesetFlags&FlagIncludeLinkToExternalFile != 0,
		IncludeTilesInsideFile:    c.TilesetFlags&FlagIncludeTilesInsideFile != 0,
		TileIDZeroAsEmptyTile:     c.TilesetFlags&FlagTileIDZeroAsEmptyTile != 0,
		XFlipAutoMatch:            c.TilesetFlags&FlagXFlipAutoMatch != 0,
		YFlipAutoMatch:            c.TilesetFlags&FlagYFlipAutoMatch != 0,
		DiagonalFlipAutoMatch:     c.TilesetFlags&FlagDiagonalFlipAutoMatch != 0,
	}
}

// CelDataType represents the type of data in the cel.
type CelDataType WORD

const (
	RawImageData CelDataType = iota
	LinkedCelData
	CompressedImageData
	CompressedTilemapData
)

// Chunk0x2005 determines where to put a cel in the specified layer/frame.
type Chunk0x2005 struct {
	LayerIndex   WORD        `json:"layer_index"`   // Layer index (2 bytes) // 2 bytes so far
	XPosition    SHORT       `json:"x_position"`    // X position (2 bytes) // 4 bytes so far
	YPosition    SHORT       `json:"y_position"`    // Y position (2 bytes) // 6 bytes so far
	OpacityLevel BYTE        `json:"opacity_level"` // Opacity level (0-255) (1 byte) // 7 bytes so far
	CelType      CelDataType `json:"cel_type"`      // Cel Type (2 bytes) // (9 bytes so far)
	ZIndex       SHORT       `json:"z_index"`       // Z-Index (2 bytes) // 11 bytes so far
	Reserved     [5]BYTE     `json:"reserved"`      // Reserved for future use (5 bytes) // 16 bytes so far
	Data         []byte      `json:"data"`          // Data of the chunk (variable length) // (variable length)
}

type RawImage struct {
	Width  WORD
	Height WORD
	Pixels []BYTE
}

type LinkedCel struct {
	FramePosition WORD
}

type CompressedImage struct {
	Width  WORD
	Height WORD
	Pixels []BYTE
}

type CompressedTilemap struct {
	Width               WORD     // Width in number of Tiles (2 bytes)
	Height              WORD     // Height in number of Tiles (2 bytes) // 4 bytes so far
	BitsPerTile         WORD     // Bits per tile (at the moment it's always 32-bit per tile) (2 bytes) // 6 bytes so far
	TileIDBitmask       DWORD    // Bitmask for tile ID  lower 29 bits (e.g. 0x1fffffff for 32-bit tiles) (4 bytes) // 10 bytes
	XFlipBitmask        DWORD    // Bitmask for X flip highest bit (e.g. 0x80000000) (4 bytes) // 14 bytes so far
	YFlipBitmask        DWORD    // Bitmask for Y flip second highest bit (e.g. 0x40000000) (4 bytes) // 18 bytes so far
	DiagonalFlipBitmask DWORD    // Bitmask for diagonal flip 3rd highest bit (e.g. 0x20000000) (4 bytes) // 22 bytes so far
	Reserved            [10]BYTE // Reserved for future use (10 bytes) // 32 bytes so far
	Tiles               []BYTE   // Compressed tile data using ZLIB. They are in Row by row, from top to bottom tile by tile
}

// Function to decompress ZLIB data
func decompressZlib(data []byte) ([]byte, error) {
	// Check if the input data is not empty
	if len(data) == 0 {
		return nil, fmt.Errorf("input data is empty")
	}

	// Print the data
	// fmt.Printf("         >>> Decompressing %d bytes of ZLIB data...\n", len(data))

	// Create a bytes.Reader from the input data:
	b := bytes.NewReader(data)

	// Create a new ZLIB reader:
	r, err := zlib.NewReader(b)
	if err != nil {
		return nil, fmt.Errorf("failed to create zlib reader: %w", err)
	}
	defer r.Close()

	// Create a bytes.Buffer to hold the decompressed data:
	var out bytes.Buffer

	// Copy the decompressed data from the ZLIB reader to the buffer:
	_, err = io.Copy(&out, r)
	if err != nil {
		return nil, fmt.Errorf("failed to copy decompressed data: %w", err)
	}

	// Return the decompressed data as a byte slice:
	return out.Bytes(), nil
}

// Layer represents a layer with a specific z-index for a cel in a frame.
type Layer2005 struct {
	LayerIndex WORD  `json:"layer_index"` // Layer index (2 bytes)
	ZIndex     SHORT `json:"z_index"`     // Z-Index (2 bytes)
	Name       string
}

// Layers is a slice of Layer structs.
type Layers2005 []Layer2005

// Order calculates the order value for sorting.
func (l Layer2005) Order() int {
	return int(l.LayerIndex) + int(l.ZIndex)
}

// ProcessZIndexes processes and sorts layers based on their z-index.
func ProcessZIndexes(layers Layers2005) {
	// Check if all layers have z-index = 0
	noZIndex := true
	for _, layer := range layers {
		if layer.ZIndex != 0 {
			noZIndex = false
			break
		}
	}
	if noZIndex {
		return
	}

	// Sort layers based on their order and z-index using slices.SortFunc
	slices.SortFunc(layers, func(a, b Layer2005) int {
		orderA := a.Order()
		orderB := b.Order()
		if orderA == orderB {
			return cmp.Compare(a.ZIndex, b.ZIndex)
		}
		return cmp.Compare(orderA, orderB)
	})
}

// PrintLayers prints the layers in a formatted way.
func PrintLayers(layers Layers2005) {
	fmt.Println("Layer name and hierarchy      Layer index")
	fmt.Println("-----------------------------------------------")
	for i, layer := range layers {
		indent := strings.Repeat("  ", int(layer.LayerIndex))
		prefix := "- "
		if i > 0 && layers[i-1].LayerIndex < layer.LayerIndex {
			prefix = "`- "
		} else if i > 0 && layers[i-1].LayerIndex == layer.LayerIndex {
			prefix = "|- "
		}
		fmt.Printf("%s%s%s                  %d\n", indent, prefix, layer.Name, layer.LayerIndex)
	}
}

// Function to parse Chunk0x2005 data from a byte slice
func parseChunk0x2005(data []byte) (*Chunk0x2005, error) {
	var chunk Chunk0x2005
	reader := bytes.NewReader(data)

	l := reader.Len()
	size := reader.Size()
	// fmt.Println("     - Starting reading", l, "bytes of data")

	// Read common fields
	if err := binary.Read(reader, binary.LittleEndian, &chunk.LayerIndex); err != nil {
		return nil, err
	}

	l = reader.Len()
	if l != int(size-2) {
		return nil, fmt.Errorf("invalid data length: %d", l)
	}

	// fmt.Println("      > Layer Index:", chunk.LayerIndex)

	if err := binary.Read(reader, binary.LittleEndian, &chunk.XPosition); err != nil {
		return nil, err
	}

	l = reader.Len()
	if l != int(size-4) {
		return nil, fmt.Errorf("invalid data length: %d", l)
	}

	// fmt.Println("      > X Position:", chunk.XPosition)

	if err := binary.Read(reader, binary.LittleEndian, &chunk.YPosition); err != nil {
		return nil, err
	}

	l = reader.Len()
	if l != int(size-6) {
		return nil, fmt.Errorf("invalid data length: %d", l)
	}

	// fmt.Println("      > Y Position:", chunk.YPosition)

	if err := binary.Read(reader, binary.LittleEndian, &chunk.OpacityLevel); err != nil {
		return nil, err
	}

	l = reader.Len()
	if l != int(size-7) {
		return nil, fmt.Errorf("invalid data length: %d", l)
	}

	// fmt.Println("      > Opacity Level:", chunk.OpacityLevel)

	if err := binary.Read(reader, binary.LittleEndian, &chunk.CelType); err != nil {
		return nil, err
	}

	l = reader.Len()
	if l != int(size-9) {
		return nil, fmt.Errorf("invalid data length: %d", l)
	}

	// celtype := "Unknown"
	// switch chunk.CelType {
	// case RawImageData:
	// 	celtype = "Raw Image"
	// case LinkedCelData:
	// 	celtype = "Linked Cel"
	// case CompressedImageData:
	// 	celtype = "Compressed Image"
	// case CompressedTilemapData:
	// 	celtype = "Compressed Tilemap"
	// }

	// fmt.Println("      > Cel Type:", celtype)

	if err := binary.Read(reader, binary.LittleEndian, &chunk.ZIndex); err != nil {
		return nil, err
	}

	l = reader.Len()
	if l != int(size-11) {
		return nil, fmt.Errorf("invalid data length: %d", l)
	}

	// ztype := "Unknown"
	// if chunk.ZIndex < 0 {
	// 	ztype = fmt.Sprintf("-N: Show this cel %d layers back", chunk.ZIndex)
	// } else if chunk.ZIndex > 0 {
	// 	ztype = fmt.Sprintf("+N: Show this cel %d layers later", chunk.ZIndex)
	// } else {
	// 	ztype = "0: Default layer ordering"
	// }

	// fmt.Println("      > Z-Index:", ztype)

	if err := binary.Read(reader, binary.LittleEndian, &chunk.Reserved); err != nil {
		return nil, err
	}

	l = reader.Len()
	if l != int(size-16) {
		return nil, fmt.Errorf("invalid data length: %d", l)
	}

	// fmt.Println("      > Reserved:", chunk.Reserved)

	// Read the actual data based on the remaining length (size - 16)
	remainingSize := int(size) - 16
	chunk.Data = make([]byte, remainingSize)
	if err := binary.Read(reader, binary.LittleEndian, &chunk.Data); err != nil {
		return nil, fmt.Errorf("error reading data based on length %v: %v", l, err)
	}

	// Check if they are empty
	if len(chunk.Data) == 0 {
		return nil, fmt.Errorf("data is empty")
	}

	// fmt.Printf("      > %s : %d bytes\n", celtype, len(chunk.Data))

	// Read specific fields based on CelType
	switch chunk.CelType {
	case RawImageData:
		// Raw Image Data
		rawImage := RawImage{}
		rawImage.Width = WORD(chunk.Data[0]) | WORD(chunk.Data[1])<<8
		rawImage.Height = WORD(chunk.Data[2]) | WORD(chunk.Data[3])<<8
		rawImage.Pixels = chunk.Data[4:]
		fmt.Printf("      > Raw Image Data: %dx%d pixels\n", rawImage.Width, rawImage.Height)
	case LinkedCelData:
		// Linked Cel Data
		linkedCel := LinkedCel{}
		linkedCel.FramePosition = WORD(chunk.Data[0]) | WORD(chunk.Data[1])<<8
		fmt.Printf("      > Linked Cel Data: Frame Position: %d\n", linkedCel.FramePosition)
	case CompressedImageData:
		// Compressed Image Data
		compressedImage := CompressedImage{}
		compressedImage.Width = WORD(chunk.Data[0]) | WORD(chunk.Data[1])<<8
		compressedImage.Height = WORD(chunk.Data[2]) | WORD(chunk.Data[3])<<8
		compressedImage.Pixels = chunk.Data[4:]
		// fmt.Printf("      > Compressed Image Data: %dx%d pixels\n", compressedImage.Width, compressedImage.Height)

		// decompressedPixels, err := decompressZlib(compressedImage.Pixels)
		// if err != nil {
		// 	return nil, fmt.Errorf("error decompressing tile data: %v", err)
		// }

		// fmt.Printf("         >>> Decompressed Image: %d bytes\n", len(decompressedPixels))

	case CompressedTilemapData:
		// Compressed Tilemap Data
		compressedTilemap := CompressedTilemap{}
		compressedTilemap.Width = WORD(chunk.Data[0]) | WORD(chunk.Data[1])<<8
		// fmt.Printf("       >> Width in number of Tiles: %d columns\n", compressedTilemap.Width)
		compressedTilemap.Height = WORD(chunk.Data[2]) | WORD(chunk.Data[3])<<8
		// fmt.Printf("       >> Height in number of Tiles: %d rows\n", compressedTilemap.Height)
		compressedTilemap.BitsPerTile = WORD(chunk.Data[4]) | WORD(chunk.Data[5])<<8
		// fmt.Printf("       >> Bits per tile: %d\n", compressedTilemap.BitsPerTile)
		compressedTilemap.TileIDBitmask = DWORD(chunk.Data[6]) | DWORD(chunk.Data[7])<<8 | DWORD(chunk.Data[8])<<16 | DWORD(chunk.Data[9])<<24
		// fmt.Printf("       >> Tile ID Bitmask: 0x%08x\n", compressedTilemap.TileIDBitmask)
		compressedTilemap.XFlipBitmask = DWORD(chunk.Data[10]) | DWORD(chunk.Data[11])<<8 | DWORD(chunk.Data[12])<<16 | DWORD(chunk.Data[13])<<24
		// fmt.Printf("       >> X Flip Bitmask: 0x%08x\n", compressedTilemap.XFlipBitmask)
		compressedTilemap.YFlipBitmask = DWORD(chunk.Data[14]) | DWORD(chunk.Data[15])<<8 | DWORD(chunk.Data[16])<<16 | DWORD(chunk.Data[17])<<24
		// fmt.Printf("       >> Y Flip Bitmask: 0x%08x\n", compressedTilemap.YFlipBitmask)
		compressedTilemap.DiagonalFlipBitmask = DWORD(chunk.Data[18]) | DWORD(chunk.Data[19])<<8 | DWORD(chunk.Data[20])<<16 | DWORD(chunk.Data[21])<<24
		// fmt.Printf("       >> Diagonal Flip Bitmask: 0x%08x\n", compressedTilemap.DiagonalFlipBitmask)
		compressedTilemap.Reserved = [10]BYTE{chunk.Data[22], chunk.Data[23], chunk.Data[24], chunk.Data[25], chunk.Data[26], chunk.Data[27], chunk.Data[28], chunk.Data[29], chunk.Data[30], chunk.Data[31]}
		// fmt.Printf("       >> Reserved: %v\n", compressedTilemap.Reserved)
		compressedTilemap.Tiles = chunk.Data[32:]
		// fmt.Printf("       >> Size of Compressed (ZLIB) Tilemap: %d bytes\n", len(compressedTilemap.Tiles))

		// Decompress the tile zlib data
		decompressedTiles, err := decompressZlib(compressedTilemap.Tiles)
		if err != nil {
			return nil, fmt.Errorf("error decompressing tile data: %v", err)
		}

		// fmt.Printf("         >>> Decompressed Tilemap: %d bytes\n", len(decompressedTiles))

		// Update the compressed tilemap data with the decompressed data
		compressedTilemap.Tiles = decompressedTiles

		// Recondstruct the tiles
		// Row by row, from top to bottom tile by tile
		// Each tile is has Bits per tile: 32 bits (4 bytes)

		// Calculate the number of tiles
		numTiles := int(compressedTilemap.Width) * int(compressedTilemap.Height)

		// verify that the number of tiles is correct
		bytesPerTile := int(compressedTilemap.BitsPerTile) / 8
		if numTiles != len(decompressedTiles)/bytesPerTile {
			return nil, fmt.Errorf("invalid number of tiles: %d", numTiles)
		}

		// fmt.Printf("         >>> Number of Tiles: %d\n", numTiles)

		// Calculate the tilemap resolution in tile units (not pixels)
		tilemapRows := int(compressedTilemap.Height)
		tilemapCols := int(compressedTilemap.Width)

		// Print tilemap resolution
		// fmt.Printf("         >>> Tilemap Rows: %d\n", tilemapRows)
		// fmt.Printf("         >>> Tilemap Columns: %d\n", tilemapCols)

		var tiles []Tile

		// Iterate over the tiles row by row
		for row := 0; row < tilemapRows; row++ {
			// Iterate over the tiles in the row
			for col := 0; col < tilemapCols; col++ {
				// Calculate the offset in the decompressed tile data
				offset := (row*tilemapCols + col) * bytesPerTile

				// Read the tile data based on the bits per tile
				tileData := decompressedTiles[offset : offset+bytesPerTile]

				// tiledata is []4 bytes
				// Bitmasking: First, a bitmask is applied to isolate the bit of interest.
				//             For example, 0x80000000 isolates the highest bit (bit 31),
				//							0x40000000 isolates the second highest bit (bit 30),
				// 						and 0x20000000 isolates the third highest bit (bit 29).
				// Shifting: After applying the bitmask, the result is shifted right to move the bit of interest to the least significant bit (bit 0).
				//           This converts the bit into a boolean-like value (0 or 1).

				tileID := binary.LittleEndian.Uint32(tileData) & uint32(compressedTilemap.TileIDBitmask)
				xFlip := binary.LittleEndian.Uint32(tileData) & uint32(compressedTilemap.XFlipBitmask) >> 31
				yFlip := binary.LittleEndian.Uint32(tileData) & uint32(compressedTilemap.YFlipBitmask) >> 30
				diagonalFlip := binary.LittleEndian.Uint32(tileData) & uint32(compressedTilemap.DiagonalFlipBitmask) >> 29

				// Create a new tile
				tile := Tile{
					Width:        8, // 8 pixels
					Height:       8, // 8 pixels
					ID:           int(tileID),
					XFlip:        xFlip == 1,
					YFlip:        yFlip == 1,
					DiagonalFlip: diagonalFlip == 1,
				}

				// Append the tile to the list of tiles
				tiles = append(tiles, tile)
			}
		}

		// Print the tilemap
		// fmt.Println("         >>> Tilemap:")
		for row := 0; row < tilemapRows; row++ {
			rowStr := ""
			for col := 0; col < tilemapCols; col++ {
				// Calculate the index of the tile in the tiles slice
				index := row*tilemapCols + col

				// Print the tile information
				var str string
				id := tiles[index].ID
				str += fmt.Sprintf("%02d", id)
				if tiles[index].XFlip {
					str += "X"
				}
				if tiles[index].YFlip {
					str += "Y"
				}
				if tiles[index].DiagonalFlip {
					str += "D"
				}

				// Append the tile information to the row string
				rowStr += fmt.Sprintf("%-4s", str)
			}
			// fmt.Printf("           >>>> [ %s ]\n", rowStr)
		}

	}

	return &chunk, nil
}

// TODO: Not implemented yet.
// This is about color profile, like sRGB and ICC profiles, so colors are correctly displayed on different devices.

// Chunk0x2007 represents the color profile chunk
type Chunk0x2007 struct {
	Type       WORD  `json:"type"` // Color profile type // 2 bytes // so far 2 bytes
	Flags      WORD  // 1 - use special fixed gamma `json:"flags"` // 2 bytes // so far 4 bytes
	FixedGamma FIXED // Fixed gamma (1.0 = linear) `json:"fixed_gamma"` // 4 bytes // so far 8 bytes
	// Note: The gamma in sRGB is 2.2 in overall but it doesn't use this fixed gamma,
	// because sRGB uses different gamma sections (linear and non-linear).
	// If sRGB is specified with a fixed gamma = 1.0, it means that this is Linear sRGB.
	Reserved         [8]BYTE // Reserved for future use, set to zero (8 bytes) `json:"reserved"` // 8 bytes // so far 16 bytes
	ICCProfileLength DWORD   // ICC profile data length `json:"icc_profile_length,omitempty"` // 4 bytes // so far 20 bytes
	ICCProfileData   []BYTE  // ICC profile data. More info: http://www.color.org/ICC1V42.pdf `json:"icc_profile_data,omitempty"` // variable length
}

// Define a constant for the special fixed gamma flag
const UseSpecialFixedGammaFlag WORD = 1

// Define constants using iota
const (
	NoColorProfile = iota
	UseSRGB
	UseEmbeddedICCProfile
)

var colorProfileTypes = map[WORD]string{
	NoColorProfile:        "No color profile (as in old .aseprite files)",
	UseSRGB:               "Use sRGB",
	UseEmbeddedICCProfile: "Use the embedded ICC profile",
}

// GetTypeDescription returns a human-readable description of the color profile type
func (c *Chunk0x2007) GetTypeDescription() string {
	if description, exists := colorProfileTypes[c.Type]; exists {
		return description
	}
	return "Unknown color profile type"
}

// IsValid checks if the color profile type is valid
func (c *Chunk0x2007) IsValid() bool {
	return c.Type == 0 || c.Type == 1 || c.Type == 2
}

// UsesSpecialFixedGamma checks if the special fixed gamma flag is set
func (c *Chunk0x2007) UsesSpecialFixedGamma() bool {
	return c.Flags&UseSpecialFixedGammaFlag != 0
}

// PrintICCProfile prints the ICC profile length and data
func (c *Chunk0x2007) PrintICCProfile() {
	fmt.Printf("ICC Profile Length: %d\n", c.ICCProfileLength)
	fmt.Printf("ICC Profile Data: %v\n", c.ICCProfileData)
}

// parse0x2007 parses the 0x2007 chunk data
func parse0x2007(data []byte) (*Chunk0x2007, error) {
	reader := bytes.NewReader(data)
	var chunk Chunk0x2007

	if err := binary.Read(reader, binary.LittleEndian, &chunk.Type); err != nil {
		return nil, err
	}
	// fmt.Println("Color Profile Type:", chunk.GetTypeDescription())

	if err := binary.Read(reader, binary.LittleEndian, &chunk.Flags); err != nil {
		return nil, err
	}
	// fmt.Println("Uses Special Fixed Gamma:", chunk.UsesSpecialFixedGamma())

	if err := binary.Read(reader, binary.LittleEndian, &chunk.FixedGamma); err != nil {
		return nil, err
	}
	// fmt.Println("Fixed Gamma:", chunk.FixedGamma)

	if err := binary.Read(reader, binary.LittleEndian, &chunk.Reserved); err != nil {
		return nil, err
	}
	// fmt.Printf("Reserved: %v\n", chunk.Reserved)

	// If type is not ICC, then skip the ICC profile data
	if chunk.Type == UseEmbeddedICCProfile {
		if err := binary.Read(reader, binary.LittleEndian, &chunk.ICCProfileLength); err != nil {
			return nil, err
		}
		// fmt.Println("ICC Profile Length:", chunk.ICCProfileLength)

		chunk.ICCProfileData = make([]BYTE, chunk.ICCProfileLength)
		if err := binary.Read(reader, binary.LittleEndian, &chunk.ICCProfileData); err != nil {
			return nil, err
		}
		// fmt.Println("ICC Profile Data:", chunk.ICCProfileData)
	}

	if !chunk.IsChunkValid() {
		return nil, fmt.Errorf("invalid 0x2007 chunk")
	}

	return &chunk, nil
}

// IsChunkValid checks if the chunk is valid
func (c *Chunk0x2007) IsChunkValid() bool {
	// Check if the color profile type is valid
	if !c.IsValid() {
		return false
	}
	// Check if ICCProfileLength matches the length of ICCProfileData
	if c.ICCProfileLength != DWORD(len(c.ICCProfileData)) {
		return false
	}
	return true
}

type Chucnk0x2019 struct {
	NewPaletteSize DWORD   // New palette size, total number of entries (4 bytes)
	FirstColor     DWORD   // First color index to change (4 bytes)
	LastColor      DWORD   // Last color index to change (4 bytes)
	Reserved       [8]BYTE // Reserved (set to 0) (8 bytes)

}

func parseChunk0x2019(data []byte) (*Chucnk0x2019, error) {
	r := bytes.NewReader(data)

	chunk := &Chucnk0x2019{}
	if err := binary.Read(r, binary.LittleEndian, &chunk.NewPaletteSize); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.FirstColor); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.LastColor); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.Reserved); err != nil {
		return nil, err
	}

	return chunk, nil
}

const (
	// Magic number (0xF1FA)
	MagicNumberFrame = 0xF1FA
)

type Frame struct {
	Header FrameHeader
	Chunks []Chunk
}

// FrameHeader represents the structure of a frame header (16 bytes)
type FrameHeader struct {
	BytesInFrame  DWORD   // Bytes in frame (4 bytes)
	MagicNumber   WORD    // Magic number (0xF1FA) (2 bytes)
	OldChunkCount WORD    // Old field which specifies the number of "chunks" in this frame. If this value is 0xFFFF, we might have more chunks to read in this frame (so we have to use the NewChunkCount) (2 bytes)
	FrameDuration WORD    // Frame duration in milliseconds (2 bytes)
	Reserved      [2]BYTE // Reserved (set to 0) (2 bytes) for future use.
	NewChunkCount DWORD   // New field which specifies the number of "chunks" in this frame. If this is 0, use the OldChunkCount. (4 bytes)
}

// NumberOfChunks returns the number of chunks in the frame
func (fh *FrameHeader) NumberOfChunks() uint32 {
	if fh.OldChunkCount == 0xFFFF {
		return fh.NewChunkCount
	}
	if fh.NewChunkCount == 0 {
		return uint32(fh.OldChunkCount)
	}

	return uint32(fh.NewChunkCount)

}

// PrintFrameHeader prints the frame header information
// After the header come the "frames" data. Each frame has this little header of 16 bytes:
func (fh *FrameHeader) PrintFrameHeader() {
	fmt.Printf(" \nFrame Header Information:\n")
	fmt.Println("~~~~~~~~~~~~~~~~~~~~~~~~~")
	fmt.Printf("  * Bytes in Frame: %d (Header: 16 bytes, Chunks: %d bytes)\n", fh.BytesInFrame, fh.BytesInFrame-16)
	fmt.Printf("  * Magic Number: 0x%X\n", fh.MagicNumber)
	fmt.Printf("  * Old Chunk Count: %d\n", fh.OldChunkCount)
	fmt.Printf("  * Frame Duration: %d ms\n", fh.FrameDuration)
	fmt.Printf("  * Reserved: %v\n", fh.Reserved)
	fmt.Printf("  * New Chunk Count: %d\n", fh.NewChunkCount)
	fmt.Printf("  * Number of Chunks: %d\n", fh.NumberOfChunks())
}

// Chunk represents the structure of a chunk
type Chunk struct {
	ChunkSize DWORD  // Size of the chunk (4 bytes)
	ChunkType WORD   // Type of the chunk (2 bytes)
	ChunkData []BYTE // Data of the chunk (variable length)
}

// IsValid checks if the chunk size is valid
func (c *Chunk) IsValid() bool {
	// The chunk size must be at least 6 bytes (4 bytes for ChunkSize + 2 bytes for ChunkType)
	return c.ChunkSize >= 6
}

// checkFrameSize checks if the total chunk size plus frame header size equals BytesInFrame
func checkFrameSize(totalChunkSize uint32, frameHeader *FrameHeader) {
	const frameHeaderSize = 16
	if totalChunkSize+frameHeaderSize != frameHeader.BytesInFrame {
		panic(fmt.Sprintf("Frame size mismatch: expected %d, got %d", frameHeader.BytesInFrame, totalChunkSize+frameHeaderSize))
	}
}

// PrintData prints the chunk data
func (c *Chunk) PrintData() {
	fmt.Printf("Chunk Size: %d\n", c.ChunkSize)
	fmt.Printf("Chunk Type: %d\n", c.ChunkType)
	fmt.Printf("Chunk Data: %v\n", c.ChunkData)
}

// Constants
const (
	// Magic number (0xA5E0)
	MagicNumber = 0xA5E0

	// Color depth (bits per pixel)
	ColorDepthRGBA      WORD = 32
	ColorDepthGrayscale WORD = 16
	ColorDepthIndexed   WORD = 8
)

// Define the ASE header structure (128 bytes)
type Header struct {
	FileSize          DWORD    // File size (4 bytes)
	MagicNumberHeader WORD     // Magic number (0xA5E0) (2 bytes)
	FrameCount        WORD     // Number of frames (2 bytes)
	Width             WORD     // Width in pixels (2 bytes)
	Height            WORD     // Height in pixels (2 bytes)
	ColorDepth        WORD     // Color depth (bits per pixel) (2 bytes) (32 bpp = RGBA, 16 bpp = Grayscale, 8 bpp = Indexed) (2 bytes)
	Flags             DWORD    // Flags: 1 = Layer opacity has valid value (4 bytes)
	Speed             WORD     // Speed (milliseconds between frames, deprecated, DEPRECATED: You should use the frame duration field from each frame header) (2 bytes)
	Reserved1         DWORD    // Reserved (set to 0)  (4 bytes)
	Reserved2         DWORD    // Reserved (set to 0) (4 bytes)
	TransparentIdx    BYTE     // Palette entry (index) which represents transparent color in all non-background layers (only for Indexed sprites) (1 byte)
	IgnoreBytes       [3]BYTE  // Ignore these bytes (3 bytes)
	NumColors         WORD     // Number of colors (0 means 256 for old sprites format) (2 bytes)
	PixelWidth        BYTE     // Pixel width (pixel ratio is "pixel width/pixel height") (1 byte)
	PixelHeight       BYTE     // Pixel height (1 byte)
	GridX             SHORT    // X position of the grid (2 bytes)
	GridY             SHORT    // Y position of the grid (2 bytes)
	GridWidth         WORD     // Grid width (zero if there is no grid) (2 bytes)
	GridHeight        WORD     // Grid height (zero if there is no grid) (2 bytes)
	FutureUse         [84]BYTE // For future use (set to zero) (84 bytes)
}

// TODO: Initialize Magic Number (0xA5E0) as a constant

// Method to get the color depth description
func (h Header) GetColorDepthDescription() string {
	switch h.ColorDepth {
	case ColorDepthRGBA:
		return "RGBA"
	case ColorDepthGrayscale:
		return "Grayscale"
	case ColorDepthIndexed:
		return "Indexed"
	default:
		return "Unknown color depth"
	}
}

// Method to check if the layer opacity flag is set
func (h Header) IsLayerOpacityValid() bool {
	return h.Flags&1 != 0
}

// Method to get the interpreted number of colors
func (h *Header) GetNumColors() uint16 {
	if h.NumColors == 0 {
		return 256
	}
	return uint16(h.NumColors)
}

// Method to get the pixel ratio
func (h *Header) GetPixelRatio() string {
	if h.PixelWidth == 0 || h.PixelHeight == 0 {
		return "Square Pixels 1:1"
	}
	return fmt.Sprintf("%d:%d", h.PixelWidth, h.PixelHeight)
}

// Method to get the grid size
func (h *Header) GetGridSize() (uint16, uint16) {
	if h.GridWidth == 0 {
		return 16, 16
	}
	if h.GridHeight == 0 {
		return uint16(h.GridWidth), 16
	}
	return uint16(h.GridWidth), uint16(h.GridHeight)
}

// formatFileSize converts the file size to a human-readable format
func formatFileSize(size uint32) string {
	if size < 1024 {
		return fmt.Sprintf("%d bytes", size)
	} else if size < 1024*1024 {
		return fmt.Sprintf("%.1fK", float64(size)/1024)
	} else if size < 1024*1024*1024 {
		return fmt.Sprintf("%.1fM", float64(size)/(1024*1024))
	} else {
		return fmt.Sprintf("%.1fG", float64(size)/(1024*1024*1024))
	}
}

// printHeader prints the fields of the Header struct
func (header *Header) printHeader() {
	fmt.Println("Sprite Information:")
	fmt.Println("===================")
	fmt.Printf("Size: %d x %d pixels (%s)\n", header.Width, header.Height, formatFileSize(header.FileSize))
	fmt.Printf("Type: colormode %s, colors %d, depth %d bpp\n", header.GetColorDepthDescription(), header.GetNumColors(), header.ColorDepth)
	// msgFlag := "Layer opacity has invalid value"
	// if header.IsLayerOpacityValid() {
	// 	msgFlag = "Layer opacity has valid value"
	// }
	// fmt.Printf("Flags: %d (%s)\n", header.Flags, msgFlag)
	// fmt.Printf("Speed: %d ms between frames\n", header.Speed)
	fmt.Printf("Transparent Index: %d\n", header.TransparentIdx)
	fmt.Printf("Aspect Ratio: %s\n", header.GetPixelRatio())
	gridWidth, gridHeight := header.GetGridSize()
	fmt.Printf("Grid Size: %d x %d\n", gridWidth, gridHeight)
	fmt.Printf("Number of Frames: %d\n", header.FrameCount)
}

// readAsepriteFile reads and parses the header, frame headers, and chunks of an .aseprite or .ase file
func readAsepriteFile(filePath string) (*Header, []Frame, error) {
	ext := filepath.Ext(filePath)
	if ext != ".aseprite" && ext != ".ase" {
		return nil, nil, fmt.Errorf("unsupported file type: %s", ext)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	// Get the file size
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	fileSize := fileInfo.Size()

	// Read the header (128 bytes)
	header := &Header{}
	err = binary.Read(file, binary.LittleEndian, header)
	if err != nil {
		return nil, nil, err
	}

	// What is the size of the header?
	headerSize := binary.Size(header)
	if headerSize != 128 {
		return nil, nil, fmt.Errorf("invalid header size: %d", headerSize)
	}

	// Read frames
	var frames []Frame

	for i := 0; i < int(header.FrameCount); i++ {
		// Read the Frame Header (16 bytes)
		// Each frame has this little header of 16 bytes:
		// ==============================================
		frameHeader := &FrameHeader{}
		err = binary.Read(file, binary.LittleEndian, frameHeader)
		if err != nil {
			fmt.Println("Error reading frame header:", err)
			return nil, nil, err
		}

		frameHeaderSize := binary.Size(frameHeader)
		if frameHeaderSize != 16 {
			return nil, nil, fmt.Errorf("invalid frame header size: %d", frameHeaderSize)
		}
		// ==============================================

		// Read the chunks for this frame
		var chunks []Chunk
		var totalChunkSize uint32

		for j := 0; j < int(frameHeader.NumberOfChunks()); j++ {
			chunk := Chunk{}

			// Chunk size info (takes 4 bytes to store it)
			err = binary.Read(file, binary.LittleEndian, &chunk.ChunkSize)
			if err != nil {
				return nil, nil, err
			}

			// Chunk type info (takes 2 bytes to store it)
			err = binary.Read(file, binary.LittleEndian, &chunk.ChunkType)
			if err != nil {
				return nil, nil, err
			}

			// Check if the chunk is valid
			if !chunk.IsValid() {
				return nil, nil, fmt.Errorf("invalid chunk detected: size %d", chunk.ChunkSize)
			}

			chunk.ChunkData = make([]BYTE, chunk.ChunkSize-6) // 6 bytes are already read (4 bytes for ChunkSize + 2 bytes for ChunkType)
			err = binary.Read(file, binary.LittleEndian, &chunk.ChunkData)
			if err != nil {
				return nil, nil, err
			}

			// Check if the chunk size matches the length of the chunk data
			if chunk.ChunkSize != uint32(len(chunk.ChunkData)+6) {
				return nil, nil, fmt.Errorf("chunk size mismatch: expected %d, got %d", chunk.ChunkSize, len(chunk.ChunkData)+6)
			}

			// Append the chunk to the list of chunks
			chunks = append(chunks, chunk)

			// Accumulate the chunk size
			totalChunkSize += chunk.ChunkSize
		}

		// Check if the total chunk size plus frame header size equals BytesInFrame
		checkFrameSize(totalChunkSize, frameHeader)

		// Create a Frame struct and append it to the frames slice
		frame := Frame{
			Header: *frameHeader,
			Chunks: chunks,
		}
		frames = append(frames, frame)
	}

	// Check if there are any bytes left non-parsed
	currentOffset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, nil, err
	}
	if currentOffset < fileSize {
		fmt.Printf("Warning: %d bytes left non-parsed\n", fileSize-currentOffset)
		panic("Not all bytes were parsed")
	}

	return header, frames, nil
}

// From https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md#references

// Function to parse FLI color chunks
// Usage:
// fliData := []byte{11, 0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255} // Example FLI color chunk data
// colorChunk, err := parseFLIColorChunk(fliData)
// log.Printf("Parsed FLI Color Chunk: %+v\n", colorChunk)

type Chucnk0x2018 struct {
	NumberOfTags WORD    // 2 bytes
	Reserved     [8]BYTE // 8 bytes
	Tags         []Tag   // Tags (variable length)
}

// LoopAnimationDirection represents the direction of the loop animation.
type LoopAnimationDirection BYTE

const (
	Forward         LoopAnimationDirection = iota // 0 = forward
	Reverse                                       // 1 = reverse
	PingPong                                      // 2 = ping-pong
	PingPongReverse                               // 3 = ping-pong reverse
)

// RepeatTimes represents the repeat times for the animation section.
type RepeatTimes WORD

const (
	Infinite RepeatTimes = iota // 0 = Doesn't specify (plays infinite in UI, once on export, for ping-pong it plays once in each direction)
	Once                        // 1 = Plays once (for ping-pong, it plays just in one direction)
	Twice                       // 2 = Plays twice (for ping-pong, it plays once in one direction, and once in reverse)
	// n = Plays N times (for values greater than 2)
)

type Tag struct {
	FromFrame          WORD                   // Frame where the tag starts (2 bytes)
	ToFrame            WORD                   // Frame where the tag ends (2 bytes)
	AnimationDirection LoopAnimationDirection // Loop animation direction (0 = forward, 1 = reverse, 2 = ping-pong, 3 = ping-pong reverse) (1 byte)
	Repeat             RepeatTimes            // Repeat N times (2 bytes)
	Reserved           [6]BYTE                // For future (set to zero) (6 bytes)
	Deprecated         [3]BYTE                // Deprecated (set to zero) (3 bytes)
	ExtraByte          BYTE                   // Extra byte (1 byte)
	TagName            STRING                 // Tag name (variable length)
}

// From https://github.com/aseprite/aseprite/blob/main/docs/ase-file-specs.md#references

type (
	BYTE   = uint8    // An 8-bit unsigned integer value
	WORD   = uint16   // A 16-bit unsigned integer value
	SHORT  = int16    // A 16-bit signed integer value
	DWORD  = uint32   // A 32-bit unsigned integer value
	LONG   = int32    // A 32-bit signed integer value
	FIXED  = int32    // A 32-bit fixed point (16.16) value
	FLOAT  = float32  // A 32-bit single-precision value
	DOUBLE = float64  // A 64-bit double-precision value
	QWORD  = uint64   // A 64-bit unsigned integer value
	LONG64 = int64    // A 64-bit signed integer value
	UUID   = [16]BYTE // A 128-bit (16-byte) unique identifier
)

// Define the POINT type
type POINT struct {
	X LONG // X coordinate value
	Y LONG // Y coordinate value
}

// Define the SIZE type
type SIZE struct {
	Width  LONG // Width of the rectangle
	Height LONG // Height of the rectangle
}

// Define the RECT type
type RECT struct {
	Origin POINT // Origin coordinates
	Size   SIZE  // Rectangle size
}

// Define a type constraint for TILE
type TileValue interface {
	BYTE | WORD | DWORD
}

// Define a type for the mask function
type MaskFunc[T TileValue] func(T) T

// Define the TILE type using generics
type TILE[T TileValue] struct {
	Value T           // Can be BYTE, WORD, or DWORD
	Mask  MaskFunc[T] // Mask function related to the meaning of each bit
}

// Example mask functions
func byteMask(value BYTE) BYTE {
	return value & 0x0F
}

func wordMask(value WORD) WORD {
	return value & 0x00FF
}

func dwordMask(value DWORD) DWORD {
	return value & 0x0000FFFF
}

// Function to compress data using zlib
func compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	_, err := writer.Write(data)
	if err != nil {
		return nil, err
	}
	err = writer.Close()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Function to decompress data using zlib
func decompress(data []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, reader)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Define constants for FLI chunk types
const (
	FLI_COLOR_256 = 11
	FLI_COLOR_64  = 4
)

// Define a structure for FLI color chunks
type FLIColorChunk struct {
	Type   int
	Colors []PIXEL
}

// Function to parse FLI color chunks
// Usage:
// fliData := []byte{11, 0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255} // Example FLI color chunk data
// colorChunk, err := parseFLIColorChunk(fliData)
// log.Printf("Parsed FLI Color Chunk: %+v\n", colorChunk)
func parseFLIColorChunk(data []byte) (*FLIColorChunk, error) {
	if len(data) < 2 {
		return nil, errors.New("data too short")
	}

	chunkType := int(data[0])
	var colors []PIXEL

	switch chunkType {
	case FLI_COLOR_256:
		colors = make([]PIXEL, 256)
		for i := 0; i < 256; i++ {
			colors[i] = PIXEL{
				RGBA: [4]BYTE{data[1+i*3], data[2+i*3], data[3+i*3], 255},
			}
		}
	case FLI_COLOR_64:
		colors = make([]PIXEL, 64)
		for i := 0; i < 64; i++ {
			colors[i] = PIXEL{
				RGBA: [4]BYTE{data[1+i*3], data[2+i*3], data[3+i*3], 255},
			}
		}
	default:
		return nil, errors.New("unknown FLI color chunk type")
	}

	return &FLIColorChunk{
		Type:   chunkType,
		Colors: colors,
	}, nil
}

type Animation struct {
	TotalFrames int
	Index       int
	Duration    []time.Duration // how long the current frame should be displayed
	LastChange  time.Time       // is updated to the current time each time the frame changes
}

type ASEFile struct {
	State   []ASETag
	Tileset ASETileset
	Sprites Sprites
}

type ASETag struct {
	Name          string
	Tilemaps      []ASETilemap
	Frames        []*ebiten.Image
	FrameDuration [][]time.Duration
	HasAnimations bool
	Animation     Animation
}

type ASETileset struct {
	Tiles                 []image.Image
	TileHeight, TileWidth int
}

type ASETilemap struct {
	Tiles                       [][]Tile
	TilemapRows, TilemapColumns int
	NumberOfTiles               int
}

// --------------------------------------------------------- //

// ParseChunk0x2018 parses the 0x2018 chunk.
func parseChunk0x2018(data []byte) (*Chucnk0x2018, error) {
	r := bytes.NewReader(data)

	chunk := &Chucnk0x2018{}
	if err := binary.Read(r, binary.LittleEndian, &chunk.NumberOfTags); err != nil {
		return nil, err
	}
	if err := binary.Read(r, binary.LittleEndian, &chunk.Reserved); err != nil {
		return nil, err
	}

	for i := 0; i < int(chunk.NumberOfTags); i++ {
		tag := Tag{}
		if err := binary.Read(r, binary.LittleEndian, &tag.FromFrame); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &tag.ToFrame); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &tag.AnimationDirection); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &tag.Repeat); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &tag.Reserved); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &tag.Deprecated); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &tag.ExtraByte); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &tag.TagName.Length); err != nil {
			return nil, err
		}
		tag.TagName.Chars = make([]BYTE, tag.TagName.Length)
		if err := binary.Read(r, binary.LittleEndian, &tag.TagName.Chars); err != nil {
			return nil, err
		}

		chunk.Tags = append(chunk.Tags, tag)
	}

	return chunk, nil
}

func ParseAseprite(f string) (ASEFile, error) {
	asepriteFile := ASEFile{}
	tileset := ASETileset{}
	tilemaps := []ASETilemap{}
	states := []ASETag{}
	frameImages := []image.Image{}
	framesDuration := []time.Duration{}

	var palette []color.Color
	header, frames, err := readAsepriteFile(f)
	if err != nil {
		fmt.Println("Error:", err)
		return ASEFile{}, err
	}

	// Parse the palette
	for _, frame := range frames {
		framesDuration = append(framesDuration, time.Duration(frame.Header.FrameDuration)*time.Millisecond)
		for _, chunk := range frame.Chunks {

			switch chunk.ChunkType {
			case 0x0004:
				paletteChunk, err := parseChunk0x0004(chunk.ChunkData)
				if err != nil {
					fmt.Println("Error parsing 0x0004 chunk:", err)
					os.Exit(1)
				}

				for _, packet := range paletteChunk.Packets {
					for _, c := range packet.Colors {
						// Create a new color

						newRGBAColor := color.RGBA{R: c.Red, G: c.Green, B: c.Blue, A: 255}

						// 255 alpha value means: the color is fully opaque (not transparent)
						// but if the  RGB is 0, then the color is fully transparent
						if newRGBAColor.R == 0 && newRGBAColor.G == 0 && newRGBAColor.B == 0 {
							newRGBAColor.A = 0
						}

						// Append the new color to the palette
						palette = append(palette, newRGBAColor)
					}
				}

			}
		}
	}

	// fmt.Println("======================")
	// for i, k := range framesDuration {
	// 	fmt.Printf("Frame %d: %v\n", i, k)
	// }
	// fmt.Println("======================")

	// // Print palette colors
	// for i, c := range palette {
	// 	fmt.Printf("Color %d: %v\n", i, c)
	// }

	// Parse the tileset and tilemap
	for _, frame := range frames {
		for _, chunk := range frame.Chunks {

			switch chunk.ChunkType {

			case 0x2023:

				tilesetChunk, err := parseChunk0x2023(chunk.ChunkData)
				if err != nil {
					fmt.Println("Error parsing 0x2023 chunk:", err)
					os.Exit(1)
				}

				decompressed, err := decompressZlib(tilesetChunk.CompressedTilesetImage)
				if err != nil {
					return ASEFile{}, fmt.Errorf("error decompressing Tileset Image data: %v", err)
				}

				tileWidth := int(tilesetChunk.TileWidth)
				tileHeight := int(tilesetChunk.TileHeight)
				numTiles := int(tilesetChunk.NumberOfTiles)
				var tilesetTiles []byte
				tileSize := tileWidth * tileHeight

				// Loop through the decompressed data to extract each tile
				for i := 0; i < len(decompressed); i += tileSize {
					// Ensure we don't go out of bounds
					if i+tileSize > len(decompressed) {
						break
					}

					// Extract the current tile
					tile := decompressed[i : i+tileSize]

					// Append the current tile to the tilesetTile slice
					tilesetTiles = append(tilesetTiles, tile...)
				}

				if numTiles != len(tilesetTiles)/tileSize {
					return ASEFile{}, fmt.Errorf("number of tiles does not match the number of tiles extracted from the tileset image data")
				}

				// Create a  PNG image for each tile
				// Create a new RGBA image

				tileImages := make([]image.Image, numTiles)

				for tile := 0; tile < numTiles; tile++ {
					// Initialize all the pixels of the tile image to be transparent
					tileImage := image.NewRGBA(image.Rect(0, 0, tileWidth, tileHeight))

					tileSize := tileWidth * tileHeight
					start := tile * tileSize
					end := start + tileSize

					// Ensure the end index does not exceed the length of the tilesetTiles data
					if end > len(tilesetTiles) {
						fmt.Println("Tile number out of range")
						return ASEFile{}, fmt.Errorf("tile number out of range")
					}

					// Extract the tile
					isolatedTile := tilesetTiles[start:end]

					// Print the tile in a readable format
					for i := 0; i < tileHeight; i++ {
						for j := 0; j < tileWidth; j++ {
							t := isolatedTile[i*tileWidth+j]
							// fmt.Printf("%x ", t)

							// Set the pixels of the PNG Image
							// Get the color from the palette
							color := palette[t]

							// Set the pixel color in the tile image
							tileImage.Set(j, i, color)

						}
					}

					// append the image to the tileImages slice
					tileImages[tile] = tileImage
				}

				tileset = ASETileset{
					Tiles:      tileImages,
					TileHeight: tileHeight,
					TileWidth:  tileWidth,
				}

			case 0x2005:
				celChunk, err := parseChunk0x2005(chunk.ChunkData)
				if err != nil {
					fmt.Println("Error parsing 0x2005 chunk:", err)
					os.Exit(1)
				}

				// Read specific fields based on CelType
				switch celChunk.CelType {
				case RawImageData:
					// Raw Image Data
					rawImage := RawImage{}
					rawImage.Width = WORD(celChunk.Data[0]) | WORD(celChunk.Data[1])<<8
					rawImage.Height = WORD(celChunk.Data[2]) | WORD(celChunk.Data[3])<<8
					rawImage.Pixels = celChunk.Data[4:]
					// fmt.Printf("      > Raw Image Data: %dx%d pixels\n", rawImage.Width, rawImage.Height)
				case LinkedCelData:
					// Linked Cel Data
					linkedCel := LinkedCel{}
					linkedCel.FramePosition = WORD(celChunk.Data[0]) | WORD(celChunk.Data[1])<<8
					// fmt.Printf("      > Linked Cel Data: Frame Position: %d\n", linkedCel.FramePosition)
				case CompressedImageData:
					// Compressed Image Data

					// Get GetColorDepthDescription from the header
					colorDepth := header.GetColorDepthDescription()
					var bitsPerPixel int
					switch colorDepth {
					case "RGBA":
						bitsPerPixel = 32
					case "Grayscale":
						bitsPerPixel = 16
					case "Indexed":
						bitsPerPixel = 8
					default:
						bitsPerPixel = 0
					}

					if bitsPerPixel == 0 {
						return ASEFile{}, fmt.Errorf("unknown color depth: %s", colorDepth)
					}

					// fmt.Println("Color Depth:", colorDepth)
					// fmt.Println("Bits per Pixel:", bitsPerPixel)

					compressedImage := CompressedImage{}
					compressedImage.Width = WORD(celChunk.Data[0]) | WORD(celChunk.Data[1])<<8
					compressedImage.Height = WORD(celChunk.Data[2]) | WORD(celChunk.Data[3])<<8
					compressedImage.Pixels = celChunk.Data[4:]
					// fmt.Printf("      > Compressed Image Data: %dx%d pixels\n", compressedImage.Width, compressedImage.Height)

					decompressedPixels, err := decompressZlib(compressedImage.Pixels)
					if err != nil {
						return ASEFile{}, fmt.Errorf("error decompressing image data: %v", err)
					}

					var pixels []PIXEL

					rowsOfPixels := make([][]PIXEL, compressedImage.Height)
					columnsOfPixels := make([]PIXEL, compressedImage.Width)

					// Iterate over the decompressed pixels
					// Each pixel has bits per pixel: bitsPerPixel bits
					// The pixels are stored in rows, from top to bottom, left to right
					for i := 0; i < len(decompressedPixels); i += bitsPerPixel / 8 {
						// Ensure we don't go out of bounds
						if i+bitsPerPixel/8 > len(decompressedPixels) {
							break
						}

						// Extract the current pixel
						pixel := decompressedPixels[i : i+bitsPerPixel/8]

						// Depending of the bitsPerPixel, we can have different color depths
						// and store them in different ways (RGBA, Grayscale, Indexed) in pixels
						switch bitsPerPixel {
						case 32:
							// RGBA color depth
							// Each pixel is stored as 4 bytes (32 bits)
							// The order of the bytes is: RGBA (Red, Green, Blue, Alpha)
							// The color values are in the range [0, 255]
							pixels = append(pixels, PIXEL{
								RGBA: [4]BYTE{pixel[0], pixel[1], pixel[2], pixel[3]},
							})
						// case 16:
						// 	// Grayscale color depth
						// 	// Each pixel is stored as 2 bytes (16 bits)
						// 	// The color value is in the range [0, 255]
						// 	// The grayscale value is stored in the Red channel
						// 	pixels = append(pixels, PIXEL{
						// 		Grayscale: pixel[0:2],
						// 	})
						case 8:
							// Indexed color depth
							// Each pixel is stored as 1 byte (8 bits)
							// The color value is an index to the palette
							// The color value is in the range [0, 255]
							pixels = append(pixels, PIXEL{
								Indexed: pixel[0],
							})
						}
					}

					// Reconstruct the pixels
					// Row by row, from top to bottom, left to right
					for row := 0; row < int(compressedImage.Height); row++ {
						// Iterate over the pixels in the row
						for col := 0; col < int(compressedImage.Width); col++ {
							// Calculate the offset in the decompressed pixel data
							offset := row*int(compressedImage.Width) + col

							// Read the pixel
							pixel := pixels[offset]

							// Store the pixel in the columnsOfPixels slice
							columnsOfPixels[col] = pixel
						}

						// Store the columnsOfPixels slice in the rowsOfPixels slice
						rowsOfPixels[row] = make([]PIXEL, len(columnsOfPixels))
						copy(rowsOfPixels[row], columnsOfPixels)
					}

					// Save into a PNG image
					// Create a new RGBA image
					img := image.NewRGBA(image.Rect(0, 0, int(compressedImage.Width), int(compressedImage.Height)))

					// Set the pixels of the PNG Image
					for i := 0; i < int(compressedImage.Height); i++ {
						for j := 0; j < int(compressedImage.Width); j++ {
							p := rowsOfPixels[i][j]

							// Get the color from the palette
							var col color.Color
							if bitsPerPixel == 8 {
								col = palette[p.Indexed]
							} else {
								col = color.RGBA{R: p.RGBA[0], G: p.RGBA[1], B: p.RGBA[2], A: p.RGBA[3]}
							}

							// Set the pixel color in the PNG image
							img.Set(j, i, col)
						}
					}

					// Save the PNG image to a file

					// // Create a new file
					// f, err := os.Create("image.png")
					// if err != nil {
					// 	return ASEFile{}, fmt.Errorf("error creating PNG file: %v", err)
					// }

					// // Encode the PNG image
					// err = png.Encode(f, img)
					// if err != nil {
					// 	return ASEFile{}, fmt.Errorf("error encoding PNG image: %v", err)
					// }

					// // Close the file
					// err = f.Close()
					// if err != nil {
					// 	return ASEFile{}, fmt.Errorf("error closing PNG file: %v", err)
					// }

					// Append img to frameImages
					frameImages = append(frameImages, img)

				case CompressedTilemapData:
					// Compressed Tilemap Data
					compressedTilemap := CompressedTilemap{}
					compressedTilemap.Width = WORD(celChunk.Data[0]) | WORD(celChunk.Data[1])<<8
					// fmt.Printf("       >> Width in number of Tiles: %d columns\n", compressedTilemap.Width)
					compressedTilemap.Height = WORD(celChunk.Data[2]) | WORD(celChunk.Data[3])<<8
					// fmt.Printf("       >> Height in number of Tiles: %d rows\n", compressedTilemap.Height)
					compressedTilemap.BitsPerTile = WORD(celChunk.Data[4]) | WORD(celChunk.Data[5])<<8
					// fmt.Printf("       >> Bits per tile: %d\n", compressedTilemap.BitsPerTile)
					compressedTilemap.TileIDBitmask = DWORD(celChunk.Data[6]) | DWORD(celChunk.Data[7])<<8 | DWORD(celChunk.Data[8])<<16 | DWORD(celChunk.Data[9])<<24
					// fmt.Printf("       >> Tile ID Bitmask: 0x%08x\n", compressedTilemap.TileIDBitmask)
					compressedTilemap.XFlipBitmask = DWORD(celChunk.Data[10]) | DWORD(celChunk.Data[11])<<8 | DWORD(celChunk.Data[12])<<16 | DWORD(celChunk.Data[13])<<24
					// fmt.Printf("       >> X Flip Bitmask: 0x%08x\n", compressedTilemap.XFlipBitmask)
					compressedTilemap.YFlipBitmask = DWORD(celChunk.Data[14]) | DWORD(celChunk.Data[15])<<8 | DWORD(celChunk.Data[16])<<16 | DWORD(celChunk.Data[17])<<24
					// fmt.Printf("       >> Y Flip Bitmask: 0x%08x\n", compressedTilemap.YFlipBitmask)
					compressedTilemap.DiagonalFlipBitmask = DWORD(celChunk.Data[18]) | DWORD(celChunk.Data[19])<<8 | DWORD(celChunk.Data[20])<<16 | DWORD(celChunk.Data[21])<<24
					// fmt.Printf("       >> Diagonal Flip Bitmask: 0x%08x\n", compressedTilemap.DiagonalFlipBitmask)
					compressedTilemap.Reserved = [10]BYTE{celChunk.Data[22], celChunk.Data[23], celChunk.Data[24], celChunk.Data[25], celChunk.Data[26], celChunk.Data[27], celChunk.Data[28], celChunk.Data[29], celChunk.Data[30], celChunk.Data[31]}
					// fmt.Printf("       >> Reserved: %v\n", compressedTilemap.Reserved)
					compressedTilemap.Tiles = celChunk.Data[32:]
					// fmt.Printf("       >> Size of Compressed (ZLIB) Tilemap: %d bytes\n", len(compressedTilemap.Tiles))

					// Decompress the tile zlib data
					decompressedTiles, err := decompressZlib(compressedTilemap.Tiles)
					if err != nil {
						return ASEFile{}, fmt.Errorf("error decompressing tile data: %v", err)
					}

					// fmt.Printf("         >>> Decompressed Tilemap: %d bytes\n", len(decompressedTiles))

					// Update the compressed tilemap data with the decompressed data
					compressedTilemap.Tiles = decompressedTiles

					// Recondstruct the tiles
					// Row by row, from top to bottom tile by tile
					// Each tile is has Bits per tile: 32 bits (4 bytes)

					// Calculate the number of tiles
					numTiles := int(compressedTilemap.Width) * int(compressedTilemap.Height)

					// verify that the number of tiles is correct
					bytesPerTile := int(compressedTilemap.BitsPerTile) / 8
					if numTiles != len(decompressedTiles)/bytesPerTile {
						return ASEFile{}, fmt.Errorf("invalid number of tiles: %d", numTiles)
					}
					tilemap := &ASETilemap{
						Tiles:          make([][]Tile, numTiles),
						TilemapRows:    int(compressedTilemap.Height),
						TilemapColumns: int(compressedTilemap.Width),
						NumberOfTiles:  numTiles,
					}
					// fmt.Printf("         >>> Number of Tiles: %d\n", numTiles)

					// Calculate the tilemap resolution in tile units (not pixels)
					tilemapRows := int(compressedTilemap.Height)
					tilemapCols := int(compressedTilemap.Width)

					// Print tilemap resolution
					// fmt.Printf("         >>> Tilemap Rows: %d\n", tilemapRows)
					// fmt.Printf("         >>> Tilemap Columns: %d\n", tilemapCols)

					var tiles []Tile

					// Iterate over the tiles row by row
					for row := 0; row < tilemapRows; row++ {
						// Iterate over the tiles in the row
						for col := 0; col < tilemapCols; col++ {
							// Calculate the offset in the decompressed tile data
							offset := (row*tilemapCols + col) * bytesPerTile

							// Read the tile data based on the bits per tile
							tileData := decompressedTiles[offset : offset+bytesPerTile]

							// tiledata is []4 bytes
							// Bitmasking: First, a bitmask is applied to isolate the bit of interest.
							//             For example, 0x80000000 isolates the highest bit (bit 31),
							//							0x40000000 isolates the second highest bit (bit 30),
							// 						and 0x20000000 isolates the third highest bit (bit 29).
							// Shifting: After applying the bitmask, the result is shifted right to move the bit of interest to the least significant bit (bit 0).
							//           This converts the bit into a boolean-like value (0 or 1).

							tileID := binary.LittleEndian.Uint32(tileData) & uint32(compressedTilemap.TileIDBitmask)
							xFlip := binary.LittleEndian.Uint32(tileData) & uint32(compressedTilemap.XFlipBitmask) >> 31
							yFlip := binary.LittleEndian.Uint32(tileData) & uint32(compressedTilemap.YFlipBitmask) >> 30
							diagonalFlip := binary.LittleEndian.Uint32(tileData) & uint32(compressedTilemap.DiagonalFlipBitmask) >> 29

							// Create a new tile
							tile := Tile{
								Width:        8, // 8 pixels
								Height:       8, // 8 pixels
								ID:           int(tileID),
								XFlip:        xFlip == 1,
								YFlip:        yFlip == 1,
								DiagonalFlip: diagonalFlip == 1,
								Image:        tileset.Tiles[tileID],
							}

							// Append the tile to the list of tiles
							tiles = append(tiles, tile)
						}
					}

					// Print the tilemap
					// fmt.Println("         >>> Tilemap:")

					// Ensure the Tilemap is initialized
					tilemap.Tiles = make([][]Tile, tilemapRows) // Outer slice with 'rows' number of elements

					for i := range tilemap.Tiles {
						tilemap.Tiles[i] = make([]Tile, tilemapCols) // Inner slice with 'cols' number of elements
					}

					// Iterate over the tiles row by row

					for row := 0; row < tilemapRows; row++ {
						rowStr := ""
						for col := 0; col < tilemapCols; col++ {
							// Calculate the index of the tile in the tiles slice
							index := row*tilemapCols + col

							// Print the tile information
							var str string
							id := tiles[index].ID
							str += fmt.Sprintf("%02d", id)
							if tiles[index].XFlip {
								str += "X"
							}
							if tiles[index].YFlip {
								str += "Y"
							}
							if tiles[index].DiagonalFlip {
								str += "D"
							}

							// Append the tile information to the row string
							rowStr += fmt.Sprintf("%-4s", str)

							// fmt.Printf("%v ", tiles[index].ID)
							tilemap.Tiles[row][col] = tiles[index]

						}
						// fmt.Println()
					}

					tilemaps = append(tilemaps, *tilemap)

				}

			}

		}
	}

	for _, frame := range frames {

		for _, chunk := range frame.Chunks {

			switch chunk.ChunkType {

			case 0x2018:
				// Tags Chunk
				tagsChunk, err := parseChunk0x2018(chunk.ChunkData)
				if err != nil {
					fmt.Println("Error parsing 0x2018 chunk:", err)
					os.Exit(1)
				}

				for stateIndex, tag := range tagsChunk.Tags {
					name := string(tag.TagName.Chars)
					from := tag.FromFrame
					to := tag.ToFrame
					state := ASETag{
						Name: name,
					}

					for i := from; i <= to; i++ {
						if len(tilemaps) != 0 {
							state.Tilemaps = append(state.Tilemaps, tilemaps[i])
						}

						if len(frameImages) != 0 {
							state.Frames = append(state.Frames, ebiten.NewImageFromImage(frameImages[i]))
						}
					}

					// Calculate the number of frames for the current state
					numFrames := to - from + 1

					// Initialize the inner slice for the current state
					state.FrameDuration = make([][]time.Duration, len(tagsChunk.Tags))

					// Populate the inner slice with the appropriate elements from framesDuration
					state.FrameDuration[stateIndex] = make([]time.Duration, numFrames)
					for i := 0; i < int(numFrames); i++ {
						state.FrameDuration[stateIndex][i] = framesDuration[int(from)+i]
					}

					if len(state.Frames) > 1 {
						state.HasAnimations = true

						state.Animation = Animation{
							TotalFrames: len(state.Frames),
							Index:       0,
							LastChange:  time.Now(),
							Duration:    state.FrameDuration[stateIndex],
						}
					}

					states = append(states, state)
				}
			}
		}
	}

	asepriteFile.Tileset = tileset

	asepriteFile.State = states
	// for stateIdx, state := range states {
	// 	for
	// }

	return asepriteFile, nil
}
