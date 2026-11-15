import { readFileSync, writeFileSync } from 'node:fs';

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8');
let template = read('./organization-chart.template.html');
const model = read('./order-model.json');
const layout = read('./order-layout.mjs');
const font = readFileSync(new URL('./assets/commissioner.woff2', import.meta.url)).toString('base64');
const exports = layout.match(/export function /g) ?? [];
if (exports.length !== 4 || layout.includes('</script')) throw new Error('Unexpected layout module shape');
const values = {
  __FONT_DATA__: font,
  __ORDER_DATA__: JSON.stringify(JSON.parse(model)).replace(/</g, '\\u003c').replace(/>/g, '\\u003e').replace(/&/g, '\\u0026'),
  __ORDER_LAYOUT__: layout.replace(/export function /g, 'function ')
};
for (const [key, value] of Object.entries(values)) {
  if (template.split(key).length !== 2) throw new Error(`Expected one ${key} marker`);
  template = template.replace(key, value);
}
writeFileSync(new URL('./organization-chart.html', import.meta.url), template);
console.log(`Built ${Buffer.byteLength(template)} bytes; ${JSON.parse(model).roles.length} standing positions`);
