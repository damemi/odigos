import { VERIFY_TOKEN } from '@/graphql';
import { useUserStore } from '@/store';
import { GeneralResult } from '@/types';
import { useMutation } from '@apollo/client';
import { CenterThis, FadeLoader } from '@odigos/ui-kit/components';
import { usePathname, useRouter } from 'next/navigation';
import { FC, ReactNode, useEffect } from 'react';

interface ProtectedRouteProps {
  children: ReactNode;
  signInPath: string;
}

const ProtectedRoute: FC<ProtectedRouteProps> = ({ children, signInPath }) => {
  const router = useRouter();
  const pathname = usePathname();
  const { token } = useUserStore();
  const isSignInPath = pathname === signInPath;

  const [verifyToken, { loading }] = useMutation<{ verifyToken: GeneralResult }, { token: string }>(VERIFY_TOKEN, {
    onError: (error) => {
      console.error(error);
      router.push(signInPath);
    },
    onCompleted: (data) => {
      if (!data.verifyToken.success) {
        router.push(signInPath);
      }
    },
  });

  useEffect(() => {
    if (!isSignInPath) {
      // If not on sign in path, check the token
      if (token) {
        verifyToken({ variables: { token } });
      } else {
        router.push(signInPath);
      }
    }
  }, [isSignInPath, token]);

  if (!isSignInPath && (loading || !token)) {
    return (
      <CenterThis style={{ height: '100vh' }}>
        <FadeLoader scale={2} />
      </CenterThis>
    );
  }

  return children;
};

export default ProtectedRoute;
