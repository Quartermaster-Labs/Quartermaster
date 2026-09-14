import { writable, derived } from "svelte/store";

const chatStreaming = writable(false);
const imageGenerating = writable(false);
const speechGenerating = writable(false);
const audioTranscribing = writable(false);
const videoGenerating = writable(false);
const threeDGenerating = writable(false);

export const playgroundActivity = derived(
  [chatStreaming, imageGenerating, speechGenerating, audioTranscribing, videoGenerating, threeDGenerating],
  ([$chat, $image, $speech, $audio, $video, $threeD]) => $chat || $image || $speech || $audio || $video || $threeD
);

export const playgroundStores = {
  chatStreaming,
  imageGenerating,
  speechGenerating,
  audioTranscribing,
  videoGenerating,
  threeDGenerating,
};
