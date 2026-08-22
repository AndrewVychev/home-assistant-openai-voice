export function createUsage() {
  return {
    inputTokens: 0,
    inputTextTokens: 0,
    inputAudioTokens: 0,
    cachedTokens: 0,
    cachedTextTokens: 0,
    cachedAudioTokens: 0,
    outputTokens: 0,
    outputTextTokens: 0,
    outputAudioTokens: 0,
  };
}

export function addRealtimeUsage(total, usage = {}) {
  const input = usage.input_token_details || {};
  const cached = input.cached_tokens_details || {};
  const output = usage.output_token_details || {};
  return {
    inputTokens: total.inputTokens + Number(usage.input_tokens || 0),
    inputTextTokens: total.inputTextTokens + Number(input.text_tokens || 0),
    inputAudioTokens: total.inputAudioTokens + Number(input.audio_tokens || 0),
    cachedTokens: total.cachedTokens + Number(input.cached_tokens || 0),
    cachedTextTokens: total.cachedTextTokens + Number(cached.text_tokens || 0),
    cachedAudioTokens: total.cachedAudioTokens + Number(cached.audio_tokens || 0),
    outputTokens: total.outputTokens + Number(usage.output_tokens || 0),
    outputTextTokens: total.outputTextTokens + Number(output.text_tokens || 0),
    outputAudioTokens: total.outputAudioTokens + Number(output.audio_tokens || 0),
  };
}

export function calculateRealtimeCost(usage, rates) {
  if (!rates) return null;
  const uncachedText = Math.max(0, usage.inputTextTokens - usage.cachedTextTokens);
  const uncachedAudio = Math.max(0, usage.inputAudioTokens - usage.cachedAudioTokens);
  const perMillion = 1_000_000;
  const input = (
    uncachedText * rates.textInput +
    usage.cachedTextTokens * rates.textCached +
    uncachedAudio * rates.audioInput +
    usage.cachedAudioTokens * rates.audioCached
  ) / perMillion;
  const output = (
    usage.outputTextTokens * rates.textOutput +
    usage.outputAudioTokens * rates.audioOutput
  ) / perMillion;
  return { input, output, total: input + output };
}
