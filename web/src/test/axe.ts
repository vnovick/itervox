import axe from 'axe-core';

/**
 * Runs axe-core against a rendered container and returns the violations as
 * `rule-id: target` strings, so a failing assertion names the rule and node.
 *
 * `color-contrast` is off because jsdom has no layout or computed colours;
 * contrast is asserted numerically in src/__tests__/tokens.contrast.test.ts.
 * `region` is off because components are rendered outside a page landmark.
 */
export async function axeViolations(container: Element): Promise<string[]> {
  const result = await axe.run(container, {
    rules: {
      'color-contrast': { enabled: false },
      region: { enabled: false },
    },
  });
  return result.violations.flatMap((v) => v.nodes.map((n) => `${v.id}: ${n.target.join(' ')}`));
}
