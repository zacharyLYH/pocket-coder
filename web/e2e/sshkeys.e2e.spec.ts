import { expect, test } from '@playwright/test'
import { deleteAllSSHKeys } from './helpers'

// SSH key management flows against the real backend: adding keys, deleting
// them, duplicate rejection, and server-side validation. Each test cleans
// the key registry before and after, so order never matters.

test.describe('SSH key management flow', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIWorkLaptopKey'

  async function addKey(page: import('@playwright/test').Page, key: string, label = '') {
    await page.getByPlaceholder(/ssh-ed25519/).fill(key)
    if (label) await page.getByPlaceholder(/Label/).fill(label)
    await page.getByRole('button', { name: 'Add key' }).click()
  }

  test.beforeEach(async ({ request }) => {
    await deleteAllSSHKeys(request)
  })

  test.afterEach(async ({ request }) => {
    await deleteAllSSHKeys(request)
  })

  test('add a key and see it in the list', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByText('No keys registered.')).toBeVisible()

    await addKey(page, KEY, 'work-laptop')

    await expect(page.getByText('work-laptop')).toBeVisible()
    await expect(page.getByText('No keys registered.')).not.toBeVisible()
    await expect(page).toHaveScreenshot('ssh-keys-one-key.png')
  })

  test('delete a key removes it from the list', async ({ page }) => {
    await page.request.post('/api/ssh-keys', { data: { publicKey: KEY, label: 'my-key' } })
    await page.goto('/')
    await expect(page.getByText('my-key')).toBeVisible()

    await page.getByRole('button', { name: 'Delete' }).click()
    await expect(page.getByText('my-key')).not.toBeVisible()
    await expect(page.getByText('No keys registered.')).toBeVisible()
  })

  test('duplicate key is rejected with error', async ({ page }) => {
    await page.request.post('/api/ssh-keys', { data: { publicKey: KEY } })
    await page.goto('/')
    await expect(page.getByText('No keys registered.')).not.toBeVisible()

    await addKey(page, KEY)

    await expect(page.getByText('key already registered')).toBeVisible()
    await expect(page).toHaveScreenshot('ssh-keys-duplicate-error.png')
  })

  test('invalid key format shows error', async ({ page }) => {
    await page.goto('/')

    await addKey(page, 'not-a-real-key')

    await expect(page.getByText(/not a valid SSH public key/)).toBeVisible()
    await expect(page.getByText('No keys registered.')).toBeVisible()
  })
})

test.describe('SSH keys on phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('SSH keys section renders on phone', async ({ page }) => {
    await deleteAllSSHKeys(page.request)
    await page.request.post('/api/ssh-keys', { data: { publicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKeyOne11111', label: 'laptop' } })
    await page.request.post('/api/ssh-keys', { data: { publicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKeyTwo22222', label: 'desktop' } })

    try {
      await page.goto('/')
      await expect(page.getByText('laptop')).toBeVisible()
      await expect(page.getByText('desktop')).toBeVisible()
      // the card sits below the fold on a phone — shoot THE CARD, not the
      // viewport, so the shot is actually of SSH keys and does not shift
      // when other cards above it change
      const card = page.locator('[data-slot="card"]', { hasText: 'SSH Keys' })
      await card.scrollIntoViewIfNeeded()
      await expect(card).toHaveScreenshot('ssh-keys-phone.png')
    } finally {
      await deleteAllSSHKeys(page.request)
    }
  })
})
