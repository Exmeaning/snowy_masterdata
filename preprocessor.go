package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ───────────────────────────────────────────────────────────────
// Go port of the Python costume preprocessor
// ───────────────────────────────────────────────────────────────

var maleCharacterIDs = map[int]bool{
	12: true, 13: true, 16: true, 23: true, 26: true,
}

var cosGroupRegex = regexp.MustCompile(`^(cos\d+)_`)
var colorSuffixRegex = regexp.MustCompile(`_(\d{2})$`)

// ─── Data structures ──────────────────────────────────────────

type Costume3D struct {
	ID                 int    `json:"id"`
	Costume3DGroupID   *int   `json:"costume3dGroupId,omitempty"`
	Costume3DType      string `json:"costume3dType,omitempty"`
	Name               string `json:"name"`
	PartType           string `json:"partType"`
	ColorID            int    `json:"colorId,omitempty"`
	ColorName          string `json:"colorName,omitempty"`
	AssetbundleName    string `json:"assetbundleName"`
	CharacterID        int    `json:"characterId"`
	Costume3DRarity    string `json:"costume3dRarity,omitempty"`
	Designer           string `json:"designer,omitempty"`
	PublishedAt        *int64 `json:"publishedAt,omitempty"`
	ArchivePublishedAt *int64 `json:"archivePublishedAt,omitempty"`
}

type CardCostume3D struct {
	CardID      int `json:"cardId"`
	Costume3dID int `json:"costume3dId"`
}

type ShopCost struct {
	ResourceType string `json:"resourceType,omitempty"`
	ResourceID   int    `json:"resourceId,omitempty"`
	Quantity     int    `json:"quantity,omitempty"`
}

type Costume3DShopItem struct {
	ID              int        `json:"id"`
	GroupID         *int       `json:"groupId,omitempty"`
	HeadCostume3dID *int       `json:"headCostume3dId,omitempty"`
	BodyCostume3dID *int       `json:"bodyCostume3dId,omitempty"`
	Costs           []ShopCost `json:"costs,omitempty"`
	StartAt         *int64     `json:"startAt,omitempty"`
}

type PartVariant struct {
	ColorID         int    `json:"colorId"`
	ColorName       string `json:"colorName"`
	AssetbundleName string `json:"assetbundleName"`
}

type ShopInfo struct {
	ShopItemID  int        `json:"shopItemId"`
	ShopGroupID *int       `json:"shopGroupId,omitempty"`
	Costs       []ShopCost `json:"costs,omitempty"`
	StartAt     *int64     `json:"startAt,omitempty"`
}

type CostumeEntry struct {
	ID                 int                      `json:"id"`
	Costume3DGroupID   *int                     `json:"costume3dGroupId"`
	Costume3DType      string                   `json:"costume3dType"`
	Name               string                   `json:"name"`
	PartTypes          []string                 `json:"partTypes"`
	CharacterIDs       []int                    `json:"characterIds"`
	Gender             string                   `json:"gender"`
	Costume3DRarity    string                   `json:"costume3dRarity"`
	CostumePrefix      string                   `json:"costumePrefix"`
	Designer           string                   `json:"designer"`
	PublishedAt        *int64                   `json:"publishedAt,omitempty"`
	ArchivePublishedAt *int64                   `json:"archivePublishedAt,omitempty"`
	Parts              map[string][]PartVariant `json:"parts"`
	Source             string                   `json:"source"`
	CardIDs            []int                    `json:"cardIds,omitempty"`
	ShopInfo           *ShopInfo                `json:"shopInfo,omitempty"`
}

type OutputStats struct {
	Total      int            `json:"total"`
	BySource   map[string]int `json:"by_source"`
	ByPartType map[string]int `json:"by_partType"`
	ByGender   map[string]int `json:"by_gender"`
	ByRarity   map[string]int `json:"by_rarity"`
}

type Output struct {
	Stats    OutputStats    `json:"stats"`
	Costumes []CostumeEntry `json:"costumes"`
}

// ─── Core logic ───────────────────────────────────────────────

func getCostumeGroupPrefix(assetName string) string {
	match := cosGroupRegex.FindStringSubmatch(assetName)
	if match != nil {
		return match[1]
	}
	return colorSuffixRegex.ReplaceAllString(assetName, "")
}

func buildEntry(group []Costume3D, prefix, source string) CostumeEntry {
	// Sort by colorId, then by id
	sort.Slice(group, func(i, j int) bool {
		ci := group[i].ColorID
		cj := group[j].ColorID
		if ci == 0 {
			ci = 1
		}
		if cj == 0 {
			cj = 1
		}
		if ci != cj {
			return ci < cj
		}
		return group[i].ID < group[j].ID
	})

	rep := group[0]

	// Build parts: partType -> deduplicated color-variant list
	parts := make(map[string][]PartVariant)
	seenAssets := make(map[string]map[string]bool) // partType -> assetName -> seen

	for _, c := range group {
		pt := c.PartType
		asset := c.AssetbundleName

		if seenAssets[pt] == nil {
			seenAssets[pt] = make(map[string]bool)
		}

		if !seenAssets[pt][asset] {
			seenAssets[pt][asset] = true
			colorID := c.ColorID
			if colorID == 0 {
				colorID = 1
			}
			parts[pt] = append(parts[pt], PartVariant{
				ColorID:         colorID,
				ColorName:       c.ColorName,
				AssetbundleName: asset,
			})
		}
	}

	// Unique character IDs
	charIDSet := make(map[int]bool)
	for _, c := range group {
		charIDSet[c.CharacterID] = true
	}
	charIDs := make([]int, 0, len(charIDSet))
	for id := range charIDSet {
		charIDs = append(charIDs, id)
	}
	sort.Ints(charIDs)

	// Determine gender
	gender := "female"
	allMale := true
	for _, cid := range charIDs {
		if !maleCharacterIDs[cid] {
			allMale = false
			break
		}
	}
	if allMale {
		gender = "male"
	}

	// Sorted part types
	partTypes := make([]string, 0, len(parts))
	for pt := range parts {
		partTypes = append(partTypes, pt)
	}
	sort.Strings(partTypes)

	rarity := rep.Costume3DRarity
	if rarity == "" {
		rarity = "normal"
	}

	cosType := rep.Costume3DType
	if cosType == "" {
		cosType = "normal"
	}

	return CostumeEntry{
		ID:                 rep.ID,
		Costume3DGroupID:   rep.Costume3DGroupID,
		Costume3DType:      cosType,
		Name:               rep.Name,
		PartTypes:          partTypes,
		CharacterIDs:       charIDs,
		Gender:             gender,
		Costume3DRarity:    rarity,
		CostumePrefix:      prefix,
		Designer:           rep.Designer,
		PublishedAt:        rep.PublishedAt,
		ArchivePublishedAt: rep.ArchivePublishedAt,
		Parts:              parts,
		Source:             source,
	}
}

func RunPreprocessor(repoDir string) error {
	masterDir := filepath.Join(repoDir, "master")

	log.Println("[Preprocessor] Loading master data files...")

	// Load costume3ds.json
	costume3ds, err := loadJSONFile[[]Costume3D](filepath.Join(masterDir, "costume3ds.json"))
	if err != nil {
		return fmt.Errorf("load costume3ds.json: %w", err)
	}

	// Load cardCostume3ds.json
	cardCostume3ds, err := loadJSONFile[[]CardCostume3D](filepath.Join(masterDir, "cardCostume3ds.json"))
	if err != nil {
		return fmt.Errorf("load cardCostume3ds.json: %w", err)
	}

	// Load costume3dShopItems.json
	shopItems, err := loadJSONFile[[]Costume3DShopItem](filepath.Join(masterDir, "costume3dShopItems.json"))
	if err != nil {
		return fmt.Errorf("load costume3dShopItems.json: %w", err)
	}

	// ── Build lookup maps ──────────────────────────────────────

	costumeByID := make(map[int]Costume3D)
	for _, c := range *costume3ds {
		costumeByID[c.ID] = c
	}

	// ── Step 1: Card-sourced costume3d IDs ─────────────────────

	cardIDToCostumeIDs := make(map[int]map[int]bool)
	for _, entry := range *cardCostume3ds {
		if cardIDToCostumeIDs[entry.CardID] == nil {
			cardIDToCostumeIDs[entry.CardID] = make(map[int]bool)
		}
		cardIDToCostumeIDs[entry.CardID][entry.Costume3dID] = true
	}

	// ── Step 2: Shop-sourced costume3d IDs ─────────────────────

	shopItemLookup := make(map[int]Costume3DShopItem)
	for _, item := range *shopItems {
		if item.HeadCostume3dID != nil {
			shopItemLookup[*item.HeadCostume3dID] = item
		}
		if item.BodyCostume3dID != nil {
			shopItemLookup[*item.BodyCostume3dID] = item
		}
	}

	// ── Step 3: Group ALL costumes by prefix ───────────────────

	prefixGroup := make(map[string][]Costume3D)
	costumeToPrefix := make(map[int]string)

	for _, c := range *costume3ds {
		prefix := getCostumeGroupPrefix(c.AssetbundleName)
		prefixGroup[prefix] = append(prefixGroup[prefix], c)
		costumeToPrefix[c.ID] = prefix
	}

	// ── Step 4: Process card-sourced costumes ──────────────────

	processed := make(map[string]CostumeEntry)
	seenPrefixes := make(map[string]bool)

	cardPrefixToCardIDs := make(map[string]map[int]bool)
	for _, entry := range *cardCostume3ds {
		if _, ok := costumeByID[entry.Costume3dID]; !ok {
			continue
		}
		prefix := costumeToPrefix[entry.Costume3dID]
		if cardPrefixToCardIDs[prefix] == nil {
			cardPrefixToCardIDs[prefix] = make(map[int]bool)
		}
		cardPrefixToCardIDs[prefix][entry.CardID] = true
	}

	for prefix, cardIDSet := range cardPrefixToCardIDs {
		if seenPrefixes[prefix] {
			continue
		}
		seenPrefixes[prefix] = true

		group := prefixGroup[prefix]
		if len(group) == 0 {
			continue
		}

		entry := buildEntry(group, prefix, "card")

		cardIDs := make([]int, 0, len(cardIDSet))
		for id := range cardIDSet {
			cardIDs = append(cardIDs, id)
		}
		sort.Ints(cardIDs)
		entry.CardIDs = cardIDs

		processed[prefix] = entry
	}

	// ── Step 5: Process shop-sourced costumes ──────────────────

	shopPrefixToItems := make(map[string][]Costume3DShopItem)
	for _, item := range *shopItems {
		for _, cidPtr := range []*int{item.HeadCostume3dID, item.BodyCostume3dID} {
			if cidPtr == nil {
				continue
			}
			cid := *cidPtr
			if _, ok := costumeByID[cid]; !ok {
				continue
			}
			prefix := costumeToPrefix[cid]
			shopPrefixToItems[prefix] = append(shopPrefixToItems[prefix], item)
		}
	}

	for prefix, items := range shopPrefixToItems {
		if seenPrefixes[prefix] {
			continue
		}
		seenPrefixes[prefix] = true

		group := prefixGroup[prefix]
		if len(group) == 0 {
			continue
		}

		si := items[0]
		entry := buildEntry(group, prefix, "shop")
		entry.ShopInfo = &ShopInfo{
			ShopItemID:  si.ID,
			ShopGroupID: si.GroupID,
			Costs:       si.Costs,
			StartAt:     si.StartAt,
		}

		processed[prefix] = entry
	}

	// ── Step 6: Process remaining costumes ─────────────────────

	for prefix, group := range prefixGroup {
		if seenPrefixes[prefix] {
			continue
		}
		seenPrefixes[prefix] = true

		if len(group) == 0 {
			continue
		}

		rep := group[0]
		source := "other"
		if strings.Contains(rep.AssetbundleName, "default") {
			source = "default"
		}

		entry := buildEntry(group, prefix, source)
		processed[prefix] = entry
	}

	// ── Step 7: Build final output ─────────────────────────────

	result := make([]CostumeEntry, 0, len(processed))
	for _, entry := range processed {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID < result[j].ID
	})

	// ── Step 8: Build stats ────────────────────────────────────

	stats := OutputStats{
		Total:      len(result),
		BySource:   make(map[string]int),
		ByPartType: make(map[string]int),
		ByGender:   make(map[string]int),
		ByRarity:   make(map[string]int),
	}

	for _, item := range result {
		stats.BySource[item.Source]++
		for _, pt := range item.PartTypes {
			stats.ByPartType[pt]++
		}
		stats.ByGender[item.Gender]++
		stats.ByRarity[item.Costume3DRarity]++
	}

	output := Output{
		Stats:    stats,
		Costumes: result,
	}

	// ── Step 9: Write output ───────────────────────────────────

	outputPath := filepath.Join(masterDir, "snowy_costumes.json")
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal output: %w", err)
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	log.Printf("[Preprocessor] ✅ Written %s", outputPath)
	log.Printf("[Preprocessor]    Total unique costumes: %d", stats.Total)
	log.Printf("[Preprocessor]    By source:    %v", stats.BySource)
	log.Printf("[Preprocessor]    By partType:  %v", stats.ByPartType)
	log.Printf("[Preprocessor]    By gender:    %v", stats.ByGender)
	log.Printf("[Preprocessor]    By rarity:    %v", stats.ByRarity)
	log.Printf("[Preprocessor]    File size:    %.1f KB", float64(len(data))/1024)

	return nil
}

func loadJSONFile[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &result, nil
}
