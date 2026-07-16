import assert from "node:assert/strict";
import { pickWidgetSuffix, WIDGET_SUFFIXES } from "../components/widget/widgetCopy";

for (const [locale, items] of Object.entries(WIDGET_SUFFIXES)) {
  assert.ok(items.length >= 40, `${locale} requires at least 40 widget suffixes`);
  assert.equal(new Set(items).size, items.length, `${locale} widget suffixes must be unique`);
  const max = locale === "en" ? 22 : 10;
  assert.ok(items.every((item) => Array.from(item).length <= max), `${locale} contains an overlong widget suffix`);
  assert.notEqual(pickWidgetSuffix(items, items[0], () => 0), items[0], `${locale} must avoid immediate repeats`);
}

console.log("widget i18n tests passed");
