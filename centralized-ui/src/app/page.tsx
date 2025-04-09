'use client'

import { useEffect } from 'react'
import { useRouter } from 'next/navigation'
import { ROUTES } from '@/utils'
import styled from 'styled-components'
import { CenterThis, FadeLoader } from '@odigos/ui-kit/components'

const Container = styled(CenterThis)`
  height: 100vh;
`

export default function App() {
  const router = useRouter()

  useEffect(() => {
    router.push(ROUTES.CONNECTIONS)
  }, [router])

  return (
    <Container>
      <FadeLoader scale={2} />
    </Container>
  )
}
