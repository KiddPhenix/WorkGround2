// widgetSkins.ts — static registry of widget visual skins with nine-slice shell
// resources. New skins live under assets/widget-mode/skins/<id>; classic keeps
// using the original pager-shell assets so existing installs stay compatible.
//
// Adding a new skin:
// 1. Create the skin directory with shell.9/ (9 PNG tiles), shell.png, and preview.png.
// 2. Import the 9 tiles as a static array below.
// 3. Add the skin entry to WIDGET_SKINS with its id and tiles.

import classicTopLeft from "../../assets/widget-mode/pager-shell.9/top-left.png";
import classicTop from "../../assets/widget-mode/pager-shell.9/top.png";
import classicTopRight from "../../assets/widget-mode/pager-shell.9/top-right.png";
import classicLeft from "../../assets/widget-mode/pager-shell.9/left.png";
import classicCenter from "../../assets/widget-mode/pager-shell.9/center.png";
import classicRight from "../../assets/widget-mode/pager-shell.9/right.png";
import classicBottomLeft from "../../assets/widget-mode/pager-shell.9/bottom-left.png";
import classicBottom from "../../assets/widget-mode/pager-shell.9/bottom.png";
import classicBottomRight from "../../assets/widget-mode/pager-shell.9/bottom-right.png";

import bpTopLeft from "../../assets/widget-mode/skins/bp/shell.9/top-left.png";
import bpTop from "../../assets/widget-mode/skins/bp/shell.9/top.png";
import bpTopRight from "../../assets/widget-mode/skins/bp/shell.9/top-right.png";
import bpLeft from "../../assets/widget-mode/skins/bp/shell.9/left.png";
import bpCenter from "../../assets/widget-mode/skins/bp/shell.9/center.png";
import bpRight from "../../assets/widget-mode/skins/bp/shell.9/right.png";
import bpBottomLeft from "../../assets/widget-mode/skins/bp/shell.9/bottom-left.png";
import bpBottom from "../../assets/widget-mode/skins/bp/shell.9/bottom.png";
import bpBottomRight from "../../assets/widget-mode/skins/bp/shell.9/bottom-right.png";

import instantTopLeft from "../../assets/widget-mode/skins/instant/shell.9/top-left.png";
import instantTop from "../../assets/widget-mode/skins/instant/shell.9/top.png";
import instantTopRight from "../../assets/widget-mode/skins/instant/shell.9/top-right.png";
import instantLeft from "../../assets/widget-mode/skins/instant/shell.9/left.png";
import instantCenter from "../../assets/widget-mode/skins/instant/shell.9/center.png";
import instantRight from "../../assets/widget-mode/skins/instant/shell.9/right.png";
import instantBottomLeft from "../../assets/widget-mode/skins/instant/shell.9/bottom-left.png";
import instantBottom from "../../assets/widget-mode/skins/instant/shell.9/bottom.png";
import instantBottomRight from "../../assets/widget-mode/skins/instant/shell.9/bottom-right.png";

import petTopLeft from "../../assets/widget-mode/skins/pet/shell.9/top-left.png";
import petTop from "../../assets/widget-mode/skins/pet/shell.9/top.png";
import petTopRight from "../../assets/widget-mode/skins/pet/shell.9/top-right.png";
import petLeft from "../../assets/widget-mode/skins/pet/shell.9/left.png";
import petCenter from "../../assets/widget-mode/skins/pet/shell.9/center.png";
import petRight from "../../assets/widget-mode/skins/pet/shell.9/right.png";
import petBottomLeft from "../../assets/widget-mode/skins/pet/shell.9/bottom-left.png";
import petBottom from "../../assets/widget-mode/skins/pet/shell.9/bottom.png";
import petBottomRight from "../../assets/widget-mode/skins/pet/shell.9/bottom-right.png";

import recorderTopLeft from "../../assets/widget-mode/skins/recorder/shell.9/top-left.png";
import recorderTop from "../../assets/widget-mode/skins/recorder/shell.9/top.png";
import recorderTopRight from "../../assets/widget-mode/skins/recorder/shell.9/top-right.png";
import recorderLeft from "../../assets/widget-mode/skins/recorder/shell.9/left.png";
import recorderCenter from "../../assets/widget-mode/skins/recorder/shell.9/center.png";
import recorderRight from "../../assets/widget-mode/skins/recorder/shell.9/right.png";
import recorderBottomLeft from "../../assets/widget-mode/skins/recorder/shell.9/bottom-left.png";
import recorderBottom from "../../assets/widget-mode/skins/recorder/shell.9/bottom.png";
import recorderBottomRight from "../../assets/widget-mode/skins/recorder/shell.9/bottom-right.png";

export type WidgetSkinId = "classic" | "bp" | "instant" | "pet" | "recorder";

export const WIDGET_SKIN_IDS: WidgetSkinId[] = ["classic", "bp", "instant", "pet", "recorder"];

export interface WidgetSkinEntry {
	id: WidgetSkinId;
	tiles: [string, string, string, string, string, string, string, string, string];
}

/**
 * Static nine-slice tile arrays for each registered widget skin.
 * Index order: top-left, top, top-right, left, center, right, bottom-left, bottom, bottom-right.
 */
export const WIDGET_SKINS: Readonly<Record<WidgetSkinId, WidgetSkinEntry>> = {
	classic: {
		id: "classic",
		tiles: [
			classicTopLeft, classicTop, classicTopRight,
			classicLeft, classicCenter, classicRight,
			classicBottomLeft, classicBottom, classicBottomRight,
		],
	},
	bp: {
		id: "bp",
		tiles: [
			bpTopLeft, bpTop, bpTopRight,
			bpLeft, bpCenter, bpRight,
			bpBottomLeft, bpBottom, bpBottomRight,
		],
	},
	instant: {
		id: "instant",
		tiles: [
			instantTopLeft, instantTop, instantTopRight,
			instantLeft, instantCenter, instantRight,
			instantBottomLeft, instantBottom, instantBottomRight,
		],
	},
	pet: {
		id: "pet",
		tiles: [
			petTopLeft, petTop, petTopRight,
			petLeft, petCenter, petRight,
			petBottomLeft, petBottom, petBottomRight,
		],
	},
	recorder: {
		id: "recorder",
		tiles: [
			recorderTopLeft, recorderTop, recorderTopRight,
			recorderLeft, recorderCenter, recorderRight,
			recorderBottomLeft, recorderBottom, recorderBottomRight,
		],
	},
};

/** Resolve a skin id string to a valid WidgetSkinId, falling back to classic. */
export function resolveWidgetSkin(raw: string): WidgetSkinId {
	return WIDGET_SKIN_IDS.includes(raw as WidgetSkinId) ? (raw as WidgetSkinId) : "classic";
}

/** Safe helper: returns the tile array for a skin, always falling back to classic. */
export function widgetSkinTiles(skin: string): readonly string[] {
	return WIDGET_SKINS[resolveWidgetSkin(skin)]?.tiles ?? WIDGET_SKINS.classic.tiles;
}

/** Path to the lightweight settings-card preview for a skin. */
export function widgetSkinPreview(skin: WidgetSkinId): string {
	const previews: Record<WidgetSkinId, string> = {
		classic: new URL("../../assets/widget-mode/skins/classic/preview.png", import.meta.url).href,
		bp: new URL("../../assets/widget-mode/skins/bp/preview.png", import.meta.url).href,
		instant: new URL("../../assets/widget-mode/skins/instant/preview.png", import.meta.url).href,
		pet: new URL("../../assets/widget-mode/skins/pet/preview.png", import.meta.url).href,
		recorder: new URL("../../assets/widget-mode/skins/recorder/preview.png", import.meta.url).href,
	};
	return previews[skin];
}
