// Base CSS of the renderer, injected once per document. Only GPU-friendly properties are
// animated (transform, opacity). No filters, no blur, no soft shadows (Pi 3 rule).

export const RENDER_CSS = `
.os-stage{position:absolute;inset:0;overflow:hidden;background:#000;contain:strict}
.os-canvas{position:absolute;left:50%;top:50%;overflow:hidden;transform-origin:0 0}
.os-layer{position:absolute;inset:0}
.os-bg{position:absolute;inset:0;background-repeat:no-repeat;background-position:center}
.os-bg video,.os-el video,.os-el img{display:block;width:100%;height:100%}
.os-content{position:absolute;inset:0}
.os-el{position:absolute;box-sizing:border-box;transform-origin:50% 50%}
.os-text{display:flex;flex-direction:column;overflow:hidden;white-space:pre-wrap;overflow-wrap:break-word}
.os-text[data-valign=top]{justify-content:flex-start}
.os-text[data-valign=middle]{justify-content:center}
.os-text[data-valign=bottom]{justify-content:flex-end}
.os-p{margin:0;min-height:1em}
.os-bullet{display:inline-block;min-width:1.2em}
.os-shape{box-sizing:border-box}
.os-svg{position:absolute;inset:0;width:100%;height:100%;overflow:visible}
.os-pl{width:100%;height:100%;overflow:hidden;box-sizing:border-box}
.os-pl-cat{break-inside:avoid-column}
.os-pl-h,.os-pl-sh{break-after:avoid-column}
.os-row{position:relative;display:grid;grid-template-columns:minmax(0,1fr) auto auto;align-items:baseline;break-inside:avoid-column}
.os-row[data-price=left]{grid-template-columns:auto minmax(0,1fr) auto}
.os-row .os-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.os-row .os-logo{display:inline-block;vertical-align:baseline;height:calc(.72em * var(--logo-scale,1));height:calc(1cap * var(--logo-scale,1));width:auto;max-width:100%;object-fit:contain;object-position:left bottom}
.os-row>.os-name{grid-row:1;grid-column:1}
.os-row>.os-qty{grid-row:1;grid-column:2}
.os-row>.os-price{grid-row:1;grid-column:3}
.os-row[data-price=left]>.os-price{grid-column:1}
.os-row[data-price=left]>.os-name{grid-column:2}
.os-row[data-price=left]>.os-qty{grid-column:3}
.os-row .os-desc{grid-row:2;grid-column:1 / -1}
.os-row .os-vars{grid-row:2;grid-column:1 / -1}
.os-row .os-desc~.os-vars{grid-row:3}
.os-var{white-space:nowrap}
.os-var-soldout{position:relative}
.os-var-soldout>span{opacity:.42}
.os-var-soldout::after{content:"";position:absolute;left:0;right:0;top:50%;height:var(--vbar,3px);margin-top:calc(var(--vbar,3px) / -2);background:#d64545}
.os-row.os-soldout>*{opacity:.42}
.os-row.os-soldout::after{content:"";grid-row:1;grid-column:1 / -1;align-self:center;height:var(--bar,4px);background:#d64545;opacity:1;z-index:1}
.os-ticker{overflow:hidden;white-space:nowrap;display:flex;align-items:center}
.os-ticker-track{display:inline-flex;white-space:nowrap;will-change:transform}
.os-ticker-track>span{padding-right:4em}
.os-clock{display:flex;flex-direction:column;overflow:hidden;white-space:nowrap}
.os-hidden{visibility:hidden}
`;

let injected = false;

/** Injects the renderer CSS once into the document. */
export function injectRenderCss(doc: Document = document): void {
  if (injected && doc === document) return;
  const style = doc.createElement('style');
  style.dataset.os = 'render';
  style.textContent = RENDER_CSS;
  doc.head.appendChild(style);
  if (doc === document) injected = true;
}
