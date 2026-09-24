import { expect, test } from '@playwright/test'
import { deleteAllSSHKeys } from './helpers'

// SSH key management flows against the real backend: adding keys, deleting
// them, duplicate rejection, and server-side validation. Keys live in the
// Connections → SSH keys dialog. Each test cleans the key registry before
// and after, so order never matters.

test.describe('SSH key management flow', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  const KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIWorkLaptopKey'

  async function openDialog(page: import('@playwright/test').Page) {
    await page.goto('/')
    await page.getByTestId('setup-ssh').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    return dialog
  }

  async function addKey(dialog: import('@playwright/test').Locator, key: string, label = '') {
    await dialog.getByPlaceholder(/ssh-ed25519/).fill(key)
    if (label) await dialog.getByPlaceholder(/Label/).fill(label)
    await dialog.getByRole('button', { name: 'Add key' }).click()
  }

  test.beforeEach(async ({ request }) => {
    await deleteAllSSHKeys(request)
  })

  test.afterEach(async ({ request }) => {
    await deleteAllSSHKeys(request)
  })

  test('add a key and see it in the list', async ({ page }) => {
    const dialog = await openDialog(page)
    await expect(dialog.getByText('No keys registered.')).toBeVisible()

    await addKey(dialog, KEY, 'work-laptop')

    await expect(dialog.getByText('work-laptop')).toBeVisible()
    await expect(dialog.getByText('No keys registered.')).not.toBeVisible()
    await expect(dialog).toHaveScreenshot('ssh-keys-one-key.png')
  })

  test('delete a key removes it from the list', async ({ page }) => {
    await page.request.post('/api/ssh-keys', { data: { publicKey: KEY, label: 'my-key' } })
    const dialog = await openDialog(page)
    await expect(dialog.getByText('my-key')).toBeVisible()

    await dialog.getByRole('button', { name: 'Delete' }).click()
    await expect(dialog.getByText('my-key')).not.toBeVisible()
    await expect(dialog.getByText('No keys registered.')).toBeVisible()
  })

  test('duplicate key is rejected with error', async ({ page }) => {
    await page.request.post('/api/ssh-keys', { data: { publicKey: KEY } })
    const dialog = await openDialog(page)
    await expect(dialog.getByText('No keys registered.')).not.toBeVisible()

    await addKey(dialog, KEY)

    await expect(dialog.getByText('key already registered')).toBeVisible()
    await expect(dialog).toHaveScreenshot('ssh-keys-duplicate-error.png')
  })

  test('invalid key format shows error', async ({ page }) => {
    const dialog = await openDialog(page)

    await addKey(dialog, 'not-a-real-key')

    await expect(dialog.getByText(/not a valid SSH public key/)).toBeVisible()
    await expect(dialog.getByText('No keys registered.')).toBeVisible()
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
      await page.getByTestId('setup-ssh').click()
      const dialog = page.getByRole('dialog')
      await expect(dialog.getByText('laptop')).toBeVisible()
      await expect(dialog.getByText('desktop')).toBeVisible()
      await expect(dialog).toHaveScreenshot('ssh-keys-phone.png')
    } finally {
      await deleteAllSSHKeys(page.request)
    }
  })
})
