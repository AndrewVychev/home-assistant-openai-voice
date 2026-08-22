import test from "node:test";
import assert from "node:assert/strict";
import { actionStateMatches, validateEntityAction } from "../lib/guard.js";

test("allows direct control of lights", () => {
  const result = validateEntityAction({ entityId: "light.bedroom_lamp", action: "turn_on" });
  assert.equal(result.ok, true);
});

test("rejects direct control of locks", () => {
  const result = validateEntityAction({ entityId: "lock.front_door", action: "turn_on" });
  assert.equal(result.ok, false);
});

test("limits climate temperature", () => {
  const result = validateEntityAction({
    entityId: "climate.living_room",
    action: "set_temperature",
    temperature: 45,
  });
  assert.equal(result.ok, false);
});

test("accepts an active climate mode as successful turn_on", () => {
  assert.equal(
    actionStateMatches(
      { domain: "climate", action: "turn_on" },
      { state: "fan_only", attributes: {} },
    ),
    true,
  );
  assert.equal(
    actionStateMatches(
      { domain: "climate", action: "turn_on" },
      { state: "off", attributes: {} },
    ),
    false,
  );
});
