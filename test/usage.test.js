import test from "node:test";
import assert from "node:assert/strict";
import { addRealtimeUsage, calculateRealtimeCost, createUsage } from "../public/usage.js";

const rates = {
  textInput: 0.6,
  textCached: 0.06,
  textOutput: 2.4,
  audioInput: 10,
  audioCached: 0.3,
  audioOutput: 20,
};

test("adds Realtime usage including cached token details", () => {
  const usage = addRealtimeUsage(createUsage(), {
    input_tokens: 3000,
    output_tokens: 1500,
    input_token_details: {
      text_tokens: 1000,
      audio_tokens: 2000,
      cached_tokens: 900,
      cached_tokens_details: { text_tokens: 400, audio_tokens: 500 },
    },
    output_token_details: { text_tokens: 500, audio_tokens: 1000 },
  });
  assert.equal(usage.cachedTokens, 900);
  assert.equal(usage.outputAudioTokens, 1000);
});

test("prices cached and uncached modalities separately", () => {
  const usage = {
    ...createUsage(),
    inputTextTokens: 1000,
    inputAudioTokens: 2000,
    cachedTextTokens: 400,
    cachedAudioTokens: 500,
    outputTextTokens: 500,
    outputAudioTokens: 1000,
  };
  const cost = calculateRealtimeCost(usage, rates);
  assert.equal(cost.total, 0.036734);
});
