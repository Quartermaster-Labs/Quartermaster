import { writable, derived } from "svelte/store";

const chatStreaming = writable(false);
const imageGenerating = writable(false);
const speechGenerating = writable(false);
const musicGenerating = writable(false);
const audioTranscribing = writable(false);
const videoGenerating = writable(false);
const threeDGenerating = writable(false);
const decisionRunning = writable(false);

export const playgroundActivity = derived(
  [chatStreaming, imageGenerating, speechGenerating, musicGenerating, audioTranscribing, videoGenerating, threeDGenerating, decisionRunning],
  ([$chat, $image, $speech, $music, $audio, $video, $threeD, $decision]) =>
    $chat || $image || $speech || $music || $audio || $video || $threeD || $decision
);

export const playgroundStores = {
  chatStreaming,
  imageGenerating,
  speechGenerating,
  musicGenerating,
  audioTranscribing,
  videoGenerating,
  threeDGenerating,
  decisionRunning,
};
