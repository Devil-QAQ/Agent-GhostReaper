import { claimChainLayout } from "./claim-chain-layout.mjs";
self.onmessage = ({ data }) => {
  try { self.postMessage({ sequence: data.sequence, layout: claimChainLayout(data.skeleton, data.links) }); }
  catch { self.postMessage({ sequence: data.sequence, error: "读取链路失败" }); }
};
