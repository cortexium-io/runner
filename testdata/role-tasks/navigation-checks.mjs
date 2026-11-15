export async function openCampaignEditor(page, name, expect) {
  const link = page.getByRole('link', {name, exact: true});
  const href = await link.getAttribute('href');
  if (!href) throw new Error('campaign editor link has no destination');
  const destination = new URL(href, page.url()).href;
  await link.click();
  await page.waitForURL(destination);
  const nameInput = page.getByLabel('Name', {exact: true});
  await nameInput.waitFor({state: 'visible'});
  await expect(nameInput).toHaveValue(name);
  return page.url();
}
