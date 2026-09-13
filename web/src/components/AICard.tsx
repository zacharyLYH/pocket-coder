import { useState } from 'react'
import { Check, FlaskConical, Sparkles } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { api, errMsg } from '@/lib/api'

// AICard: the one global OpenAI-compatible credential (baseURL + key +
// model). Save runs a live test call server side and only writes on
// success, so a stored key always worked once.
export function AICard() {
  const [baseURL, setBaseURL] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [model, setModel] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [tested, setTested] = useState(false)
  const [busy, setBusy] = useState<'test' | 'save' | null>(null)
  const [saved, setSaved] = useState(false)

  function markDirty() {
    setTested(false)
    setSaved(false)
  }

  async function test() {
    setBusy('test')
    setError(null)
    setSaved(false)
    try {
      await api('/api/ai/test', {
        method: 'POST',
        body: JSON.stringify({ baseURL: baseURL.trim(), apiKey, model: model.trim() }),
      })
      setTested(true)
    } catch (err) {
      setError(errMsg(err))
      setTested(false)
    } finally {
      setBusy(null)
    }
  }

  async function save() {
    setBusy('save')
    setError(null)
    try {
      await api('/api/ai/config', {
        method: 'POST',
        body: JSON.stringify({ baseURL: baseURL.trim(), apiKey, model: model.trim() }),
      })
      setSaved(true)
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(null)
    }
  }

  return (
    <Card className="mt-4" data-testid="ai-card">
      <CardHeader>
        <div className="flex items-center gap-2">
          <Sparkles className="size-4 text-muted-foreground" />
          <CardTitle className="text-base">AI</CardTitle>
          {tested && <Badge variant="secondary"><Check className="size-3" /> tested</Badge>}
          {saved && <Badge variant="secondary"><Check className="size-3" /> saved</Badge>}
        </div>
        <CardDescription>One OpenAI-compatible key for codemaps. Test first, then save.</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <Input
          type="text"
          placeholder="Base URL (https://api.openai.com/v1)"
          value={baseURL}
          onChange={(e) => { setBaseURL(e.target.value); markDirty() }}
          data-testid="ai-base-url"
        />
        <Input
          type="password"
          placeholder="API key"
          value={apiKey}
          onChange={(e) => { setApiKey(e.target.value); markDirty() }}
          data-testid="ai-api-key"
        />
        <Input
          type="text"
          placeholder="Model (gpt-4o)"
          value={model}
          onChange={(e) => { setModel(e.target.value); markDirty() }}
          data-testid="ai-model"
        />
        {error && <p className="text-destructive max-h-24 overflow-auto break-all text-xs" data-testid="ai-error">{error}</p>}
        <div className="flex gap-2">
          <Button
            variant="outline"
            className="flex-1"
            disabled={busy !== null || !baseURL.trim() || !apiKey || !model.trim()}
            onClick={() => void test()}
            data-testid="ai-test"
          >
            <FlaskConical className="size-4" />
            {busy === 'test' ? 'Testing…' : 'Test'}
          </Button>
          <Button
            className="flex-1"
            disabled={busy !== null || !tested}
            onClick={() => void save()}
            data-testid="ai-save"
          >
            {busy === 'save' ? 'Saving…' : 'Save'}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
