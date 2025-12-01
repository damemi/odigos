import { type FC, type ReactNode, useEffect } from 'react';
import { VERIFY_TOKEN } from '@/graphql';
import { useUserStore } from '@/store';
import { GeneralResult } from '@/types';
import { useMutation } from '@apollo/client';
import { usePathname, useRouter } from 'next/navigation';
import { CenterThis, FadeLoader } from '@odigos/ui-kit/components';

interface ProtectedRouteProps {
  children: ReactNode;
  signInPath: string;
}

const ProtectedRoute: FC<ProtectedRouteProps> = ({ children, signInPath }) => {
  const router = useRouter();
  const pathname = usePathname();
  const { token, clearAuth } = useUserStore();

  const isOnSignInPath = pathname === signInPath;
  const goToSignIn = () => {
    clearAuth();
    router.push(signInPath);
  };

  const [verifyToken, { loading }] = useMutation<{ verifyToken: GeneralResult }, { token: string }>(VERIFY_TOKEN, {
    onError: (error) => {
      console.error(error);
      goToSignIn();
    },
    onCompleted: (data) => {
      if (!data.verifyToken.success) {
        goToSignIn();
      }
    },
  });

  useEffect(() => {
    if (isOnSignInPath) return;

    // If signed in, verify the token
    if (token) {
      verifyToken({ variables: { token } });
    } else {
      goToSignIn();
    }
  }, [isOnSignInPath, token]);

  if (!isOnSignInPath && (loading || !token)) {
    return (
      <CenterThis style={{ height: '100vh' }}>
        <FadeLoader scale={2} />
      </CenterThis>
    );
  }

  return children;
};

export default ProtectedRoute;
