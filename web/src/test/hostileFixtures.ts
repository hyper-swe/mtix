/** Child A catalog, checked against HostileIDs/HostileText by Go tests.
 * Go JSON replaces invalid UTF-8 with U+FFFD before it reaches a browser.
 */
import raw from "../../../internal/testutil/testdata/hostile-web.json?raw";

interface Fixture { Name: string; Class: string; Value: string }
export const hostileFixtures = JSON.parse(raw) as { ids: Fixture[]; text: Fixture[] };
