package release

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func browserReceiptFixture(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	observations, journeys, geometries := []any{}, []any{}, []any{}
	states := []string{"valid primary presets and secondary options", "New enters usable initial play viewport", "actual edit, Undo, Redo, saved visible state survives reload", "home resume restores saved visible edit in usable viewport", "print preview"}
	box := map[string]any{"x": 8, "y": 80, "width": 200, "height": 200}
	edit := func(game, engine string) map[string]any {
		drag := "native-verified"
		if engine == "webkit" {
			drag = "native-taps-verified; WebKit drag API unavailable"
		}
		value := map[string]any{"input": map[string]int{"touch": 3, "keyboard": 0}, "touchDrag": drag}
		if game == "Shikaku" {
			value["paint"] = map[string]any{"grid": 5, "first": 0, "last": 6, "cells": []int{0, 1, 5, 6}, "scroll": []int{0, 0, 0, 0}}
		}
		return value
	}
	for _, game := range browserGames {
		lower := lowerBrowserGame(game)
		path, key := "/"+lower+"/native-123", lower+":5:0:0"
		row := map[string]any{"choice": map[string]any{"game": lower, "size": 5}, "path": path, "identicalReopen": true}
		if lower == "suguru" {
			row["choice"].(map[string]any)["maximum"] = 5
			key += ":max:5"
		}
		if lower == "nonogram" {
			row["nonogramGeometry"] = map[string]int{"rowTextUnits": 1, "columnClues": 1}
			key += ":clues:1:1"
		}
		observations = append(observations, row)
		for _, engine := range []string{"chromium", "webkit"} {
			for _, device := range []string{"desktop", "mobile"} {
				journeys = append(journeys, map[string]any{"engine": engine, "device": device, "game": game, "path": path, "states": states, "bounds": map[string]any{"board": box, "viewport": map[string]any{"width": 320, "height": 568}}, "edit": edit(game, engine), "resumeInput": map[string]int{"touch": 1, "keyboard": 0}})
			}
			for _, width := range []int{320, 360, 390} {
				geometry := map[string]any{"engine": engine, "width": width, "key": key, "path": path, "board": box}
				if game == "Shikaku" {
					geometry["edit"] = edit(game, engine)
				}
				geometries = append(geometries, geometry)
			}
		}
	}
	return map[string]any{"contract": 2, "complete": true, "headless": true, "mockedResponses": false, "engines": []string{"chromium", "webkit"}, "journeys": journeys, "geometry": geometries}, map[string]any{"status": "passed", "choices": 9, "passed": 9, "observations": observations}
}

func TestBrowserReceiptRejectsPartialOrUntruthfulProof(t *testing.T) {
	for _, change := range []string{"none", "old contract", "missing engine", "duplicate journey", "no edit", "keyboard", "missing viewport", "duplicate geometry", "forged path", "clipped board", "missing native path", "wrong paint", "page pan", "missing large edit", "missing clue footprint"} {
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
			case "wrong paint":
				rows[20].(map[string]any)["edit"].(map[string]any)["paint"].(map[string]any)["cells"] = []int{0}
			case "page pan":
				rows[20].(map[string]any)["edit"].(map[string]any)["paint"].(map[string]any)["scroll"] = []int{0, 0, 0, 20}
			case "missing large edit":
				for _, row := range geometry {
					r := row.(map[string]any)
					if strings.HasPrefix(r["key"].(string), "shikaku:") {
						delete(r, "edit")
					}
				}
			case "missing clue footprint":
				delete(native["observations"].([]any)[2].(map[string]any), "nonogramGeometry")
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
	if err = os.WriteFile(filepath.Join(root, "browser_render.mjs"), browserRenderScript, 0600); err != nil {
		t.Fatal(err)
	}
	script := `import assert from 'node:assert/strict';import {assertPlayArea,geometryCases,geometryKey,games} from './browser_contract.mjs';
import {PageReadiness} from './browser_render.mjs';
const errorPage={},readiness=new PageReadiness({}),pageErrors=['ResizeObserver loop completed with undelivered notifications.','ResizeObserver loop completed with undelivered notifications.'];
readiness.errors.set(errorPage,pageErrors);
assert.throws(()=>readiness.assertNoErrors(errorPage),error=>error instanceof assert.AssertionError&&error.actual===pageErrors,'page errors were ignored');
assert.deepEqual(readiness.errors.get(errorPage),pageErrors,'assertion cleared browser errors');
const good={internalWidth:300,internalHeight:300,clientWidth:300,clientHeight:300,viewport:{width:360,height:640},documentWidth:360,frame:{x:8,y:60,width:344,height:350},board:{x:8,y:60,width:300,height:300},controls:[{x:8,y:370,width:60,height:40,label:'Undo'}]};
assertPlayArea(good);
for(const bad of [{...good,board:{...good.board,width:420}},{...good,board:{...good.board,y:-500}},{...good,controls:[...good.controls,{x:8,y:700,width:100,height:40,label:'Add rectangle'}]},{...good,documentWidth:500},{...good,internalWidth:420}])assert.throws(()=>assertPlayArea(bad),assert.AssertionError);
assert.throws(()=>geometryCases({status:'passed',choices:1,passed:1,observations:[{choice:{game:'shikaku',size:5},identicalReopen:true}]}),assert.AssertionError);
const observations=games.map(name=>({choice:{game:name.toLowerCase(),size:5,maximum:5,band:'easy'},path:'/'+name.toLowerCase()+'/native-1',identicalReopen:true,...(name==='Nonogram'?{nonogramGeometry:{rowTextUnits:1,columnClues:1}}:{})}));
observations.push({choice:{game:'nonogram',size:5,band:'hard'},path:'/nonogram/native-2',identicalReopen:true,nonogramGeometry:{rowTextUnits:3,columnClues:2}},{choice:{game:'nonogram',size:5,band:'medium'},path:'/nonogram/native-3',identicalReopen:true,nonogramGeometry:{rowTextUnits:1,columnClues:1}});
assert.equal(geometryCases({status:'passed',choices:11,passed:11,observations}).filter(row=>row.game==='nonogram').length,2,'different actual clue footprints collapsed, or difficulty was blindly multiplied');
assert.notEqual(geometryKey({game:'suguru',size:6,maximum:4}),geometryKey({game:'suguru',size:6,maximum:6}));
delete observations[2].nonogramGeometry;assert.throws(()=>geometryCases({status:'passed',choices:11,passed:11,observations}),assert.AssertionError);
console.log('valid layout passed; clipped board, wrong entry scroll, unreachable action, document overflow and unbound publication failed');`
	file := filepath.Join(root, "fixture.mjs")
	if err = os.WriteFile(file, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), node, file).CombinedOutput(); err != nil {
		t.Fatal(fmt.Sprintf("browser fixtures: %s: %v", out, err))
	}
}

func TestBrowserAssertionFeedbackKeepsActionableBoundedContext(t *testing.T) {
	data := []byte(`{"failure":"AssertionError: actual board is clipped\nprivate stack","failureContext":{"engine":"webkit","device":"mobile","game":"Shikaku","geometry":"shikaku:15:0:0","width":320,"height":568,"phase":"advertised geometry","board":{"x":8,"y":60,"width":420,"height":420}}}`)
	failure := browserAssertionFailure(data)
	if failure == nil || len(failure.reason) > 512 {
		t.Fatal("missing bounded source feedback", failure)
	}
	for _, part := range []string{"webkit", "Shikaku", "320x568", "advertised geometry", "actual board is clipped", "420x420"} {
		if !strings.Contains(failure.reason, part) {
			t.Fatal("missing actionable context", failure.reason)
		}
	}
	for _, kind := range []string{"TimeoutError", "BrowserRuntimeError", "Error"} {
		if browserAssertionFailure([]byte(`{"failure":"`+kind+`: launch failed"}`)) != nil {
			t.Fatal("runtime failure classified as source", kind)
		}
	}
}

func TestBrowserLayoutFailureHandoff(t *testing.T) {
	data := []byte(`{"failure":"AssertionError: actual board is vertically clipped inside its frame\nprivate stack","failureContext":{"engine":"chromium","device":"mobile","game":"Kakuro","width":320,"height":568,"phase":"initial board and controls","path":"/kakuro/fixture"},"failureDiagnostic":{"status":"complete","kind":"layout","layout":{"viewport":[320,568],"frame_xywh":[12,153,296,260],"board_xywh":[13,153,294,294],"controls_xywh":[12,423,296,136],"internal_size":[294,294],"client_size":[294,294],"document_width":320,"min_clue_font_px":11.2}}}`)
	failure := browserAssertionFailure(data)
	if failure == nil {
		t.Fatal("layout assertion lost its source correction route")
	}
	f := failure.browser
	if f.Status != "complete" || f.Kind != "layout" || f.Path != "/kakuro/fixture" ||
		f.Layout.Frame != [4]float64{12, 153, 296, 260} || f.Layout.Board != [4]float64{13, 153, 294, 294} ||
		f.Layout.Controls != [4]float64{12, 423, 296, 136} || f.Layout.MinClueFont != 11.2 {
		t.Fatalf("layout evidence incomplete: %+v", f)
	}
	encoded, err := json.Marshal(f)
	if err != nil || len(encoded) > maxBrowserFeedback || strings.Contains(string(encoded), "private stack") {
		t.Fatalf("invalid public layout feedback: %s, %v", encoded, err)
	}
}
