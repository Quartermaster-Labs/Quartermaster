import { writable, derived } from "svelte/store";

const chatStreaming = writable(false);
const imageGenerating = writable(false);
const speechGenerating = writable(false);
const audioTranscribing = writable(false);
const videoGenerating = writable(false);

export const playgroundActivity = derived(
  [chatStreaming, imageGenerating, speechGenerating, audioTranscribing, videoGenerating],
  ([$chat, $image, $speech, $audio, $video]) => $chat || $image || $speech || $audio || $video
);

export const playgroundStores = {
  chatStreaming,
  imageGenerating,
  speechGenerating,
  audioTranscribing,
  videoGenerating,
};
