import { assetNetLayout } from "./asset-net-layout.mjs";

self.onmessage = ({ data }) => {
  try { self.postMessage({ sequence: data.sequence, layout: assetNetLayout(data.assets, data.hierarchy, data.relations) }); }
  catch { self.postMessage({ sequence: data.sequence, error: "资产网布局失败" }); }
};
