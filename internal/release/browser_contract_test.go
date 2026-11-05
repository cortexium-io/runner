package release

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func browserReceiptFixture(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	observations, journeys, geometries := []any{}, []any{}, []any{}
	states := []string{"valid primary presets and secondary options", "New enters usable initial play viewport", "actual edit, Undo, Redo, saved visible state survives reload", "print preview"}
	box := map[string]any{"x": 8, "y": 80, "width": 200, "height": 200}
	for _, game := range browserGames {
		lower := lowerBrowserGame(game)
		path, key := "/"+lower+"/native-123", lower+":5:0:0"
		observations = append(observations, map[string]any{"choice": map[string]any{"game": lower, "size": 5}, "path": path, "identicalReopen": true})
		for _, engine := range []string{"chromium", "webkit"} {
			for _, device := range []string{"desktop", "mobile"} {
				drag := "native-verified"
				if engine == "webkit" {
					drag = "native-taps-verified; WebKit drag API unavailable"
				}
				journeys = append(journeys, map[string]any{"engine": engine, "device": device, "game": game, "path": path, "states": states, "bounds": map[string]any{"board": box, "viewport": map[string]any{"width": 320, "height": 568}}, "edit": map[string]any{"input": map[string]any{"touch": 3, "keyboard": 0}, "touchDrag": drag}})
			}
			for _, width := range []int{320, 360, 390} {
				geometries = append(geometries, map[string]any{"engine": engine, "width": width, "key": key, "path": path, "board": box})
			}
		}
	}
	return map[string]any{"contract": 2, "complete": true, "headless": true, "mockedResponses": false, "engines": []string{"chromium", "webkit"}, "journeys": journeys, "geometry": geometries}, map[string]any{"status": "passed", "choices": 9, "passed": 9, "observations": observations}
}

func TestBrowserReceiptRejectsPartialOrUntruthfulProof(t *testing.T) {
	for _, change := range []string{"none", "old contract", "missing engine", "duplicate journey", "no edit", "keyboard", "missing viewport", "duplicate geometry", "forged path", "clipped board", "missing native path"} {
		t.Run(change, func(t *testing.T) {
			r, native := browserReceiptFixture(t)
			rows, geometry := r["journeys"].([]any), r["geometry"].([]any)
			switch change {
			case "old contract":
				r["contract"] = 1
			case "missing engine":
				r["engines"] = []string{"chromium"}
			case "duplicate journey":
				rows[1] = rows[0]
			case "no edit":
				rows[0].(map[string]any)["states"] = []string{"print preview"}
			case "keyboard":
				rows[1].(map[string]any)["edit"].(map[string]any)["input"] = map[string]any{"touch": 1, "keyboard": 1}
			case "missing viewport":
				r["geometry"] = geometry[:len(geometry)-1]
			case "duplicate geometry":
				geometry[1] = geometry[0]
			case "forged path":
				geometry[0].(map[string]any)["path"] = "/sudoku/other"
			case "clipped board":
				geometry[0].(map[string]any)["board"] = map[string]any{"x": 8, "y": 80, "width": 420, "height": 200}
			case "missing native path":
				delete(native["observations"].([]any)[0].(map[string]any), "path")
			}
			data, _ := json.Marshal(r)
			catalogue, _ := json.Marshal(native)
			err := validateBrowserReceipt(data, catalogue)
			if (err == nil) != (change == "none") {
				t.Fatal("incorrect proof verdict", err)
			}
		})
	}
}

func TestBrowserGeometryAndInputContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for browser adapter unit fixtures")
	}
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "browser_contract.mjs"), browserContractScript, 0600); err != nil {
		t.Fatal(err)
	}
	script := `import assert from 'node:assert/strict';import {assertPlayArea,geometryCases} from './browser_contract.mjs';
const good={internalWidth:300,internalHeight:300,clientWidth:300,clientHeight:300,viewport:{width:360,height:640},documentWidth:360,frame:{x:8,y:60,width:344,height:350},board:{x:8,y:60,width:300,height:300},controls:[{x:8,y:370,width:60,height:40,label:'Undo'}]};
assertPlayArea(good);
for(const bad of [{...good,board:{...good.board,width:420}},{...good,board:{...good.board,y:-500}},{...good,controls:[...good.controls,{x:8,y:700,width:100,height:40,label:'Add rectangle'}]},{...good,documentWidth:500}])assert.throws(()=>assertPlayArea(bad),assert.AssertionError);
assert.throws(()=>geometryCases({status:'passed',choices:1,passed:1,observations:[{choice:{game:'shikaku',size:5},identicalReopen:true}]}),assert.AssertionError);
console.log('valid layout passed; clipped board, wrong entry scroll, unreachable action, document overflow and unbound publication failed');`
	file := filepath.Join(root, "fixture.mjs")
	if err = os.WriteFile(file, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), node, file).CombinedOutput(); err != nil {
		t.Fatal(fmt.Sprintf("browser fixtures: %s: %v", out, err))
	}
}
